package downloader

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/redfoxius/tg-downloader/internal/storage"
	"github.com/redfoxius/tg-downloader/internal/telegram"
)

// fakeFetcher serves content[tgMediaID] and records the offsets it was asked for.
type fakeFetcher struct {
	mu      sync.Mutex
	content map[int64][]byte
	offsets map[int64]int64
	err     error
	failAt  int64 // write this many bytes then return err (0 = fail immediately when err != nil)
}

func (f *fakeFetcher) Fetch(_ context.Context, ref telegram.MediaRef, offset int64, w io.Writer) error {
	f.mu.Lock()
	f.offsets[ref.TGMediaID] = offset
	data := f.content[ref.TGMediaID]
	f.mu.Unlock()
	if f.err != nil {
		if f.failAt > 0 {
			w.Write(data[offset : offset+f.failAt])
		}
		return f.err
	}
	_, err := w.Write(data[offset:])
	return err
}

func blob(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = seed + byte(i*7)
	}
	return b
}

type env struct {
	d   *Downloader
	f   *fakeFetcher
	st  *storage.Store
	dir string
}

func setup(t *testing.T) env {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fakeFetcher{content: map[int64][]byte{}, offsets: map[int64]int64{}}
	return env{&Downloader{Fetch: f, Store: st}, f, st, t.TempDir()}
}

func (e env) queue(t *testing.T, id int64, data []byte, kind string) storage.Media {
	t.Helper()
	e.f.content[id] = data
	m := storage.Media{OwnerKind: "post", ChannelID: 1, OwnerID: 10, TGMediaID: id, Kind: kind, FileName: "lesson.mp4", Size: int64(len(data))}
	if err := e.st.AddMedia(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	return m
}

func nop(string, ...any) {}

func (e env) final(m storage.Media) string {
	return filepath.Join(e.dir, "media", fileName(m))
}

func TestDownloadsAndMarksDone(t *testing.T) {
	ctx, e := context.Background(), setup(t)
	data := blob(telegram.ChunkSize*2+123, 1)
	m := e.queue(t, 1, data, "video")
	if err := e.d.Run(ctx, telegram.Channel{ID: 1}, e.dir, nop); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(e.final(m))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("content mismatch: %v", err)
	}
	if _, err := os.Stat(e.final(m) + ".part"); !os.IsNotExist(err) {
		t.Error(".part must be gone after rename")
	}
	if p, _ := e.st.PendingMedia(ctx, 1); len(p) != 0 {
		t.Errorf("still pending: %v", p)
	}
}

func TestResumesFromAlignedOffset(t *testing.T) {
	ctx, e := context.Background(), setup(t)
	data := blob(telegram.ChunkSize*3, 2)
	m := e.queue(t, 1, data, "video")
	// A previous run left 1.5 chunks (garbage tail beyond the 1-chunk boundary).
	partial := append(append([]byte{}, data[:telegram.ChunkSize]...), bytes.Repeat([]byte{0xEE}, telegram.ChunkSize/2)...)
	os.MkdirAll(filepath.Dir(e.final(m)), 0o755)
	os.WriteFile(e.final(m)+".part", partial, 0o644)

	if err := e.d.Run(ctx, telegram.Channel{ID: 1}, e.dir, nop); err != nil {
		t.Fatal(err)
	}
	if e.f.offsets[1] != telegram.ChunkSize {
		t.Errorf("resumed at %d, want %d", e.f.offsets[1], telegram.ChunkSize)
	}
	if got, _ := os.ReadFile(e.final(m)); !bytes.Equal(got, data) {
		t.Error("resumed file differs from the original")
	}
}

func TestSkipsFileAlreadyOnDisk(t *testing.T) {
	ctx, e := context.Background(), setup(t)
	data := blob(1000, 3)
	m := e.queue(t, 1, data, "video")
	os.MkdirAll(filepath.Dir(e.final(m)), 0o755)
	os.WriteFile(e.final(m), data, 0o644)
	if err := e.d.Run(ctx, telegram.Channel{ID: 1}, e.dir, nop); err != nil {
		t.Fatal(err)
	}
	if _, called := e.f.offsets[1]; called {
		t.Error("existing complete file must not be downloaded again")
	}
	if p, _ := e.st.PendingMedia(ctx, 1); len(p) != 0 {
		t.Error("existing file should be marked done")
	}
}

func TestFailureIsRecordedAndRetried(t *testing.T) {
	ctx, e := context.Background(), setup(t)
	m := e.queue(t, 1, blob(500, 4), "document")
	e.f.err = errors.New("boom")
	if err := e.d.Run(ctx, telegram.Channel{ID: 1}, e.dir, nop); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.final(m)); err == nil {
		t.Fatal("failed file must not appear under its final name")
	}
	if p, _ := e.st.PendingMedia(ctx, 1); len(p) != 1 { // failed files are retried on the next run
		t.Fatalf("failed file should stay queued: %v", p)
	}
	e.f.err = nil
	if err := e.d.Run(ctx, telegram.Channel{ID: 1}, e.dir, nop); err != nil {
		t.Fatal(err)
	}
	if p, _ := e.st.PendingMedia(ctx, 1); len(p) != 0 {
		t.Error("retry should succeed")
	}
}

func TestSizeMismatchIsAFailure(t *testing.T) {
	ctx, e := context.Background(), setup(t)
	m := e.queue(t, 1, blob(500, 5), "video")
	e.f.content[1] = blob(400, 5) // server returns less than announced
	if err := e.d.Run(ctx, telegram.Channel{ID: 1}, e.dir, nop); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.final(m)); err == nil {
		t.Error("truncated file must not be accepted")
	}
	if _, err := os.Stat(e.final(m) + ".part"); err == nil {
		t.Error("corrupt .part must be removed")
	}
}

func TestCancelKeepsPartAndPending(t *testing.T) {
	e := setup(t)
	m := e.queue(t, 1, blob(telegram.ChunkSize*2, 6), "video")
	ctx, cancel := context.WithCancel(context.Background())
	e.f.err, e.f.failAt = context.Canceled, telegram.ChunkSize
	cancel()
	if err := e.d.Run(ctx, telegram.Channel{ID: 1}, e.dir, nop); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(e.final(m)); err == nil {
		t.Error("cancelled file must not be final")
	}
	if p, _ := e.st.PendingMedia(context.Background(), 1); len(p) != 1 || p[0].Status == "failed" {
		t.Errorf("cancelled file must stay pending: %+v", p)
	}
}

func TestSettingsFilterAppliesAtDownloadTime(t *testing.T) {
	ctx, e := context.Background(), setup(t)
	e.queue(t, 1, blob(100, 7), "video")
	set := storage.DefaultSettings()
	set.DLVideo = false // tightened after the file was queued
	_ = e.st.SaveSettings(ctx, 1, set)
	if err := e.d.Run(ctx, telegram.Channel{ID: 1}, e.dir, nop); err != nil {
		t.Fatal(err)
	}
	if _, called := e.f.offsets[1]; called {
		t.Error("disabled kind must not be downloaded")
	}
}

func TestManyFilesInParallel(t *testing.T) {
	ctx, e := context.Background(), setup(t)
	for id := int64(1); id <= 8; id++ {
		e.queue(t, id, blob(2000, byte(id)), "document")
	}
	if err := e.d.Run(ctx, telegram.Channel{ID: 1}, e.dir, nop); err != nil {
		t.Fatal(err)
	}
	if p, _ := e.st.PendingMedia(ctx, 1); len(p) != 0 {
		t.Errorf("left: %d", len(p))
	}
}

func TestFileNameIsSafe(t *testing.T) {
	m := storage.Media{OwnerKind: "post", OwnerID: 1, TGMediaID: 2, FileName: "../../etc/pa:ss*wd.pdf"}
	n := fileName(m)
	if filepath.Base(n) != n || bytes.ContainsAny([]byte(n), `/\:*`) {
		t.Errorf("unsafe name %q", n)
	}
}
