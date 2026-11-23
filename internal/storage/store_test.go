package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sub", "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrateIsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.db")
	for i := 0; i < 2; i++ { // second open must not re-run 001
		s, err := Open(p)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
}

func TestSettingsDefaultsAndRoundTrip(t *testing.T) {
	ctx, s := context.Background(), open(t)
	got, err := s.Settings(ctx, 1)
	if err != nil || got != DefaultSettings() {
		t.Fatalf("defaults: %+v %v", got, err)
	}
	want := Settings{SinceDays: 30, IncludeComments: false, DLVideo: true, ExtAllow: "pdf,zip", MaxSizeMB: 500, Concurrency: 5, AutoExport: true}
	if err := s.SaveSettings(ctx, 1, want); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Settings(ctx, 1); got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestCutoff(t *testing.T) {
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	abs := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		s    Settings
		want time.Time
	}{
		{Settings{}, time.Time{}},
		{Settings{SinceDays: 10}, now.AddDate(0, 0, -10)},
		{Settings{SinceUnix: abs.Unix()}, abs},
		{Settings{SinceDays: 30, SinceUnix: abs.Unix()}, abs},                  // later wins
		{Settings{SinceDays: 5, SinceUnix: abs.Unix()}, now.AddDate(0, 0, -5)}, // later wins
	}
	for _, c := range cases {
		if got := c.s.Cutoff(now); !got.Equal(c.want) {
			t.Errorf("%+v: got %v want %v", c.s, got, c.want)
		}
	}
}

func TestUpsertsAreIdempotent(t *testing.T) {
	ctx, s := context.Background(), open(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.UpsertChannel(ctx, Channel{ID: 1, AccessHash: 9, Title: "A", DiscussionID: 77}))
	must(s.UpsertChannel(ctx, Channel{ID: 1, AccessHash: 9, Title: "A2"})) // zero must keep 77
	var disc int64
	must(s.db.QueryRow(`SELECT discussion_id FROM channels WHERE id = 1`).Scan(&disc))
	if disc != 77 {
		t.Errorf("discussion_id overwritten: %d", disc)
	}

	must(s.UpsertPost(ctx, Post{ChannelID: 1, ID: 5, Date: 100, Text: "old"}))
	must(s.UpsertPost(ctx, Post{ChannelID: 1, ID: 5, Date: 100, Text: "new", RepliesCount: 3}))
	var text string
	var n, replies int
	must(s.db.QueryRow(`SELECT count(*), max(text), max(replies_count) FROM posts`).Scan(&n, &text, &replies))
	if n != 1 || text != "new" || replies != 3 {
		t.Errorf("posts: n=%d text=%q replies=%d", n, text, replies)
	}

	must(s.UpsertUser(ctx, User{ID: 2, Name: "Bob"}))
	must(s.UpsertComment(ctx, Comment{ChannelID: 1, ID: 10, PostID: 5, AuthorID: 2, Date: 101, Text: "hi"}))
	must(s.UpsertComment(ctx, Comment{ChannelID: 1, ID: 11, PostID: 5, Date: 102, ReplyToID: 10}))
	var reply int64
	must(s.db.QueryRow(`SELECT reply_to_id FROM comments WHERE id = 11`).Scan(&reply))
	if reply != 10 {
		t.Errorf("reply_to_id = %d", reply)
	}
}

func TestMediaLifecycle(t *testing.T) {
	ctx, s := context.Background(), open(t)
	m := Media{OwnerKind: "post", ChannelID: 1, OwnerID: 5, TGMediaID: 42, Kind: "video", FileName: "a.mp4", Size: 10}
	for i := 0; i < 2; i++ { // re-sync must not duplicate
		if err := s.AddMedia(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.PendingMedia(ctx, 1)
	if err != nil || len(list) != 1 {
		t.Fatalf("pending: %v %v", list, err)
	}
	if err := s.SetMediaDone(ctx, list[0].ID, "media/a.mp4"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMedia(ctx, m); err != nil { // re-sync after download keeps 'done'
		t.Fatal(err)
	}
	if list, _ := s.PendingMedia(ctx, 1); len(list) != 0 {
		t.Errorf("done file came back as pending: %v", list)
	}
}

func TestSyncState(t *testing.T) {
	ctx, s := context.Background(), open(t)
	if st, err := s.SyncState(ctx, 1); st != (SyncState{}) || err != nil {
		t.Fatalf("empty: %+v %v", st, err)
	}
	want := SyncState{MaxPostID: 99, CoveredFrom: 1234}
	if err := s.SetSyncState(ctx, 1, want, time.Now()); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.SyncState(ctx, 1); st != want {
		t.Errorf("got %+v", st)
	}
}

func TestAllowsMedia(t *testing.T) {
	d := DefaultSettings()
	cases := []struct {
		name       string
		s          Settings
		kind, file string
		size       int64
		want       bool
	}{
		{"defaults allow all", d, "video", "a.mp4", 1 << 30, true},
		{"video off", Settings{DLDocument: true, DLPhoto: true}, "video", "a.mp4", 1, false},
		{"photo off", Settings{DLVideo: true, DLDocument: true}, "photo", "1.jpg", 1, false},
		{"ext allowed", Settings{DLDocument: true, ExtAllow: "pdf, .ZIP"}, "document", "x.Zip", 1, true},
		{"ext rejected", Settings{DLDocument: true, ExtAllow: "pdf"}, "document", "x.exe", 1, false},
		{"ext list ignores video", Settings{DLVideo: true, ExtAllow: "pdf"}, "video", "a.mp4", 1, true},
		{"too big", Settings{DLVideo: true, MaxSizeMB: 10}, "video", "a.mp4", 11 << 20, false},
		{"at limit", Settings{DLVideo: true, MaxSizeMB: 10}, "video", "a.mp4", 10 << 20, true},
	}
	for _, c := range cases {
		if got := c.s.AllowsMedia(c.kind, c.file, c.size); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestPendingMediaResolvesCommentPost(t *testing.T) {
	ctx, s := context.Background(), open(t)
	if err := s.UpsertComment(ctx, Comment{ChannelID: 1, ID: 200, PostID: 7, Date: 1}); err != nil {
		t.Fatal(err)
	}
	_ = s.AddMedia(ctx, Media{OwnerKind: "comment", ChannelID: 1, OwnerID: 200, TGMediaID: 1, Kind: "photo"})
	_ = s.AddMedia(ctx, Media{OwnerKind: "post", ChannelID: 1, OwnerID: 7, TGMediaID: 2, Kind: "video"})
	list, err := s.PendingMedia(ctx, 1)
	if err != nil || len(list) != 2 || list[0].PostID != 7 || list[1].PostID != 7 {
		t.Fatalf("%+v %v", list, err)
	}
}

func TestChannelDir(t *testing.T) {
	if got := ChannelDir("d", 5, "../evil/x"); got != filepath.Join("d", "evilx") {
		t.Errorf("username not sanitized: %s", got)
	}
	if got := ChannelDir("d", 5, ""); got != filepath.Join("d", "channel-5") {
		t.Errorf("fallback: %s", got)
	}
}
