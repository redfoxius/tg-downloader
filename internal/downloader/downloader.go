// Package downloader fetches queued media into <channel dir>/media with resume and a worker pool.
package downloader

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/redfoxius/tg-downloader/internal/storage"
	"github.com/redfoxius/tg-downloader/internal/telegram"
)

// Fetcher writes the file to w starting at offset; satisfied by telegram.Source.
type Fetcher interface {
	Fetch(ctx context.Context, ref telegram.MediaRef, offset int64, w io.Writer) error
}

type Downloader struct {
	Fetch Fetcher
	Store *storage.Store
}

// Run downloads every pending file of the channel that passes the channel's current settings,
// with settings.Concurrency workers. Files are written to <name>.part and renamed once complete,
// so an interrupted run leaves only resumable .part files. Failures are recorded per file and
// do not stop the run; cancellation is reported as ctx.Err() and leaves files pending.
func (d *Downloader) Run(ctx context.Context, ch telegram.Channel, channelDir string, logf func(string, ...any)) error {
	set, err := d.Store.Settings(ctx, ch.ID)
	if err != nil {
		return err
	}
	all, err := d.Store.PendingMedia(ctx, ch.ID)
	if err != nil {
		return err
	}
	var todo []storage.Media
	for _, m := range all { // settings may have tightened since the file was queued
		if set.AllowsMedia(m.Kind, m.FileName, m.Size) {
			todo = append(todo, m)
		}
	}
	logf("downloading %d files (%d workers)", len(todo), set.Concurrency)

	jobs := make(chan storage.Media)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done, failed := 0, 0
	for i := 0; i < max(set.Concurrency, 1); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for m := range jobs {
				err := d.one(ctx, ch, channelDir, m, logf)
				mu.Lock()
				switch {
				case err == nil:
					done++
				case ctx.Err() != nil: // cancelled: keep it pending, not failed
				default:
					failed++
					logf("FAILED %s: %v", m.FileName, err)
					_ = d.Store.SetMediaFailed(context.WithoutCancel(ctx), m.ID, err.Error())
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for _, m := range todo {
		select {
		case jobs <- m:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	logf("media done: %d ok, %d failed, %d left", done, failed, len(todo)-done-failed)
	return ctx.Err()
}

func (d *Downloader) one(ctx context.Context, ch telegram.Channel, dir string, m storage.Media, logf func(string, ...any)) error {
	rel := path.Join("media", fileName(m))
	final := filepath.Join(dir, filepath.FromSlash(rel))
	if st, err := os.Stat(final); err == nil && (m.Size == 0 || st.Size() == m.Size) {
		return d.Store.SetMediaDone(ctx, m.ID, rel) // already on disk (e.g. the database was lost)
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return err
	}
	part := final + ".part"
	var offset int64
	if st, err := os.Stat(part); err == nil {
		offset = st.Size() / telegram.ChunkSize * telegram.ChunkSize // resume on a chunk boundary
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(offset); err != nil {
		return err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}

	pw := &progress{w: f, n: offset, total: m.Size, name: m.FileName, logf: logf, last: time.Now()}
	if offset > 0 {
		logf("%s: resuming at %s", m.FileName, human(offset))
	}
	ref := telegram.MediaRef{Channel: ch, OwnerKind: m.OwnerKind, OwnerID: m.OwnerID, PostID: m.PostID, TGMediaID: m.TGMediaID}
	if err := d.Fetch.Fetch(ctx, ref, offset, pw); err != nil {
		return err
	}
	// Photo sizes are advisory; for everything else a size mismatch means a corrupt/changed file.
	if m.Kind != "photo" && m.Size > 0 && pw.n != m.Size {
		f.Close()
		os.Remove(part)
		return fmt.Errorf("size mismatch: got %d bytes, expected %d", pw.n, m.Size)
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(part, final); err != nil {
		return err
	}
	return d.Store.SetMediaDone(ctx, m.ID, rel)
}

// fileName is unique per media (ids) and safe on disk (no separators, bounded length).
func fileName(m storage.Media) string {
	base := strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, filepath.Base(m.FileName))
	if ext := filepath.Ext(base); len(base) > 100 {
		base = base[:100-len(ext)] + ext
	}
	return fmt.Sprintf("%s-%d-%d-%s", m.OwnerKind, m.OwnerID, m.TGMediaID, base)
}

// progress counts written bytes and logs at most once per 3 seconds.
type progress struct {
	w     io.Writer
	n     int64
	total int64
	name  string
	logf  func(string, ...any)
	last  time.Time
}

func (p *progress) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.n += int64(n)
	if time.Since(p.last) >= 3*time.Second {
		p.last = time.Now()
		if p.total > 0 {
			p.logf("%s: %d%% (%s / %s)", p.name, p.n*100/p.total, human(p.n), human(p.total))
		} else {
			p.logf("%s: %s", p.name, human(p.n))
		}
	}
	return n, err
}

func human(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
}
