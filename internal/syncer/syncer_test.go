package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/redfoxius/tg-downloader/internal/storage"
	"github.com/redfoxius/tg-downloader/internal/telegram"
)

type fakeSrc struct {
	posts     []telegram.Post // newest first
	err       error           // returned after streaming all posts
	gotMin    int
	gotCutoff time.Time

	counts    map[int]int                // post id -> current comment count
	threads   map[int][]telegram.Comment // post id -> comments, newest first
	fetched   []int                      // post ids whose thread was fetched
	gotMinCmt map[int]int                // post id -> minID passed to FetchComments
}

func (f *fakeSrc) ReplyCounts(_ context.Context, _ telegram.Channel, ids []int) (map[int]int, error) {
	out := map[int]int{}
	for _, id := range ids {
		out[id] = f.counts[id]
	}
	return out, nil
}

func (f *fakeSrc) FetchComments(_ context.Context, _ telegram.Channel, postID, minID int, fn func(telegram.Comment) error) error {
	f.fetched = append(f.fetched, postID)
	if f.gotMinCmt == nil {
		f.gotMinCmt = map[int]int{}
	}
	f.gotMinCmt[postID] = minID
	for _, c := range f.threads[postID] {
		if c.ID <= minID {
			break
		}
		if err := fn(c); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeSrc) FetchPosts(_ context.Context, _ telegram.Channel, minID int, cutoff time.Time, fn func(telegram.Post) error) error {
	f.gotMin, f.gotCutoff = minID, cutoff
	for _, p := range f.posts {
		if p.ID <= minID {
			break
		}
		if err := fn(p); err != nil {
			return err
		}
	}
	return f.err
}

func setup(t *testing.T) (*Syncer, *fakeSrc, *storage.Store) {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	src := &fakeSrc{}
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	return &Syncer{Src: src, Store: st, Now: func() time.Time { return now }}, src, st
}

func nop(string, ...any) {}

func post(id int, media ...telegram.Media) telegram.Post {
	return telegram.Post{ID: id, Date: time.Unix(int64(1000+id), 0), Text: "t", Media: media}
}

func TestNoForwardsNeedsConfirmation(t *testing.T) {
	s, src, _ := setup(t)
	ch := telegram.Channel{ID: 1, NoForwards: true}
	if err := s.SyncPosts(context.Background(), ch, Options{}, nop); !errors.Is(err, ErrNoForwards) {
		t.Fatalf("got %v", err)
	}
	if src.gotMin != 0 || !src.gotCutoff.IsZero() {
		t.Error("source must not be called without confirmation")
	}
	if err := s.SyncPosts(context.Background(), ch, Options{IOwnAccess: true}, nop); err != nil {
		t.Fatal(err)
	}
}

func TestFirstThenIncrementalSync(t *testing.T) {
	ctx := context.Background()
	s, src, st := setup(t)
	ch := telegram.Channel{ID: 1}
	src.posts = []telegram.Post{
		post(3, telegram.Media{TGID: 30, Kind: "video", FileName: "a.mp4", Size: 5}),
		post(2, telegram.Media{TGID: 20, Kind: "document", FileName: "x.exe", Size: 5}),
		post(1),
	}
	set := storage.DefaultSettings()
	set.ExtAllow = "pdf" // x.exe must be filtered out, the video must pass
	if err := st.SaveSettings(ctx, 1, set); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncPosts(ctx, ch, Options{}, nop); err != nil {
		t.Fatal(err)
	}
	if src.gotMin != 0 {
		t.Errorf("first sync minID = %d", src.gotMin)
	}
	if m, _ := st.PendingMedia(ctx, 1); len(m) != 1 || m[0].Kind != "video" {
		t.Errorf("media = %+v", m)
	}

	src.posts = append([]telegram.Post{post(4)}, src.posts...)
	if err := s.SyncPosts(ctx, ch, Options{}, nop); err != nil {
		t.Fatal(err)
	}
	if src.gotMin != 3 {
		t.Errorf("incremental minID = %d, want 3", src.gotMin)
	}
	if got, _ := st.SyncState(ctx, 1); got.MaxPostID != 4 {
		t.Errorf("max id = %d", got.MaxPostID)
	}
}

func TestWideningDepthForcesRescan(t *testing.T) {
	ctx := context.Background()
	s, src, st := setup(t)
	ch := telegram.Channel{ID: 1}
	src.posts = []telegram.Post{post(2), post(1)}

	narrow := storage.DefaultSettings()
	narrow.SinceDays = 10
	_ = st.SaveSettings(ctx, 1, narrow)
	if err := s.SyncPosts(ctx, ch, Options{}, nop); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncPosts(ctx, ch, Options{}, nop); err != nil || src.gotMin != 2 {
		t.Fatalf("same depth should be incremental: min=%d err=%v", src.gotMin, err)
	}

	wide := narrow
	wide.SinceDays = 0 // all history
	_ = st.SaveSettings(ctx, 1, wide)
	if err := s.SyncPosts(ctx, ch, Options{}, nop); err != nil {
		t.Fatal(err)
	}
	if src.gotMin != 0 {
		t.Errorf("widened depth must rescan, minID = %d", src.gotMin)
	}
	if got, _ := st.SyncState(ctx, 1); got.CoveredFrom != 0 {
		t.Errorf("covered_from = %d, want 0 (all history)", got.CoveredFrom)
	}
}

func TestInterruptedSyncKeepsState(t *testing.T) {
	ctx := context.Background()
	s, src, st := setup(t)
	src.posts = []telegram.Post{post(5)}
	src.err = errors.New("boom")
	if err := s.SyncPosts(ctx, telegram.Channel{ID: 1}, Options{}, nop); err == nil {
		t.Fatal("expected error")
	}
	if got, _ := st.SyncState(ctx, 1); got.MaxPostID != 0 {
		t.Errorf("state saved after failed pass: %+v", got)
	}
}

func TestCommentsIncremental(t *testing.T) {
	ctx := context.Background()
	s, src, st := setup(t)
	ch := telegram.Channel{ID: 1}
	src.posts = []telegram.Post{post(2), post(1)}
	src.counts = map[int]int{1: 2, 2: 0}
	src.threads = map[int][]telegram.Comment{1: {
		{ID: 102, Date: time.Unix(5, 0), Text: "reply", ReplyToID: 101, Author: &telegram.User{ID: 7, Name: "Bob"},
			Media: []telegram.Media{{TGID: 9, Kind: "photo", FileName: "9.jpg"}}},
		{ID: 101, Date: time.Unix(4, 0), Text: "first", Author: &telegram.User{ID: 8, Name: "Ann"}},
	}}
	if err := s.Run(ctx, ch, Options{}, nop); err != nil {
		t.Fatal(err)
	}
	if len(src.fetched) != 1 || src.fetched[0] != 1 {
		t.Fatalf("only the post with comments must be fetched: %v", src.fetched)
	}
	if m, _ := st.PendingMedia(ctx, 1); len(m) != 1 || m[0].OwnerKind != "comment" || m[0].OwnerID != 102 {
		t.Errorf("comment media: %+v", m)
	}

	// Nothing changed: no thread is fetched again.
	src.fetched = nil
	if err := s.SyncComments(ctx, ch, nop); err != nil {
		t.Fatal(err)
	}
	if len(src.fetched) != 0 {
		t.Errorf("unchanged thread refetched: %v", src.fetched)
	}

	// A new comment on an OLD post is noticed via the refreshed count, and fetched from the stored max id.
	src.counts[1] = 3
	src.threads[1] = append([]telegram.Comment{{ID: 103, Date: time.Unix(6, 0), Text: "late"}}, src.threads[1]...)
	if err := s.SyncComments(ctx, ch, nop); err != nil {
		t.Fatal(err)
	}
	if len(src.fetched) != 1 || src.gotMinCmt[1] != 102 {
		t.Errorf("fetched=%v minID=%d, want one fetch from 102", src.fetched, src.gotMinCmt[1])
	}
}

func TestCommentsDisabled(t *testing.T) {
	ctx := context.Background()
	s, src, st := setup(t)
	src.posts = []telegram.Post{post(1)}
	src.counts = map[int]int{1: 5}
	set := storage.DefaultSettings()
	set.IncludeComments = false
	_ = st.SaveSettings(ctx, 1, set)
	if err := s.Run(ctx, telegram.Channel{ID: 1}, Options{}, nop); err != nil {
		t.Fatal(err)
	}
	if len(src.fetched) != 0 {
		t.Errorf("comments fetched although disabled: %v", src.fetched)
	}
}
