package export

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/redfoxius/tg-downloader/internal/storage"
)

func nop(string, ...any) {}

type fixture struct {
	st  *storage.Store
	dir string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	st, err := storage.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// Post 1: plain lesson with a downloaded video. Posts 2+3: one album (caption on 2, photo on 3).
	// Post 4: text that tries to inject markup.
	must(st.UpsertPost(ctx, storage.Post{ChannelID: 1, ID: 1, Date: 1000, Text: "Lesson one\nbody", Entities: `[{"type":"bold","offset":0,"length":6}]`}))
	must(st.UpsertPost(ctx, storage.Post{ChannelID: 1, ID: 2, Date: 2000, Text: "Album caption", GroupedID: 77}))
	must(st.UpsertPost(ctx, storage.Post{ChannelID: 1, ID: 3, Date: 2000, GroupedID: 77}))
	must(st.UpsertPost(ctx, storage.Post{ChannelID: 1, ID: 4, Date: 3000, Text: "<script>alert(1)</script>"}))

	addDone := func(m storage.Media, path string) {
		must(st.AddMedia(ctx, m))
		list, err := st.PendingMedia(ctx, 1)
		must(err)
		for _, p := range list {
			if p.TGMediaID == m.TGMediaID {
				must(st.SetMediaDone(ctx, p.ID, path))
			}
		}
	}
	addDone(storage.Media{OwnerKind: "post", ChannelID: 1, OwnerID: 1, TGMediaID: 10, Kind: "video", FileName: "a b.mp4", Size: 5 << 20}, "media/post-1-10-a b.mp4")
	addDone(storage.Media{OwnerKind: "post", ChannelID: 1, OwnerID: 3, TGMediaID: 11, Kind: "photo", FileName: "p.jpg"}, "media/post-3-11-p.jpg")

	must(st.UpsertUser(ctx, storage.User{ID: 5, Name: "Ann", Username: "ann"}))
	must(st.UpsertComment(ctx, storage.Comment{ChannelID: 1, ID: 100, PostID: 1, AuthorID: 5, Date: 1100, Text: "question?"}))
	must(st.UpsertComment(ctx, storage.Comment{ChannelID: 1, ID: 101, PostID: 1, Date: 1200, Text: "answer", ReplyToID: 100}))
	must(st.UpsertComment(ctx, storage.Comment{ChannelID: 1, ID: 102, PostID: 1, Date: 1300, Text: "orphan reply", ReplyToID: 999}))
	return fixture{st, t.TempDir()}
}

func (f fixture) run(t *testing.T) (index string, read func(rel string) string) {
	t.Helper()
	idx, err := Run(context.Background(), f.st, 1, "My <Channel>", f.dir, nop)
	if err != nil {
		t.Fatal(err)
	}
	return idx, func(rel string) string {
		b, err := os.ReadFile(filepath.Join(f.dir, "site", rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		return string(b)
	}
}

func TestExportSite(t *testing.T) {
	f := newFixture(t)
	idx, read := f.run(t)
	if idx != filepath.Join(f.dir, "site", "index.html") {
		t.Errorf("index path %s", idx)
	}
	index := read("index.html")
	if strings.Count(index, `href="posts/`) != 3 { // lesson 1, the album, post 4
		t.Errorf("album posts must collapse into one lesson:\n%s", index)
	}
	if !(strings.Index(index, "posts/1.html") < strings.Index(index, "posts/2.html") &&
		strings.Index(index, "posts/2.html") < strings.Index(index, "posts/4.html")) {
		t.Error("lessons must be oldest first")
	}
	if !strings.Contains(index, "My &lt;Channel&gt;") || !strings.Contains(index, `href="style.css"`) {
		t.Error("title not escaped or stylesheet not relative")
	}

	p1 := read("posts/1.html")
	for _, want := range []string{
		`<video controls preload="metadata" src="../../media/post-1-10-a%20b.mp4">`, // relative, URL-escaped
		"<b>Lesson</b>",
		"<h1>Lesson one</h1>",
		"Comments (3)",
	} {
		if !strings.Contains(p1, want) {
			t.Errorf("post 1 lacks %q", want)
		}
	}
	// "answer" must be nested inside "question?"'s <li>, the orphan stays top-level.
	q, a, o := strings.Index(p1, "question?"), strings.Index(p1, "answer"), strings.Index(p1, "orphan reply")
	if !(q < a && a < o) || !strings.Contains(p1[q:a], `<ul class="thread">`) || strings.Contains(p1[a:o], `<ul class="thread">`) {
		t.Errorf("comment tree wrong (q=%d a=%d o=%d)", q, a, o)
	}
	if !strings.Contains(p1, "Ann") || !strings.Contains(p1, "@ann") {
		t.Error("author missing")
	}

	album := read("posts/2.html") // album lesson: caption from post 2, photo from post 3
	if !strings.Contains(album, "Album caption") || !strings.Contains(album, `src="../../media/post-3-11-p.jpg"`) {
		t.Errorf("album page:\n%s", album)
	}
	if !strings.Contains(album, `href="1.html"`) || !strings.Contains(album, `href="4.html"`) {
		t.Error("prev/next links missing")
	}

	if p4 := read("posts/4.html"); strings.Contains(p4, "<script>") {
		t.Error("post text must be escaped")
	}
	if _, err := os.Stat(filepath.Join(f.dir, "site.tmp")); !os.IsNotExist(err) {
		t.Error("temp dir must not remain")
	}
}

func TestExportOptions(t *testing.T) {
	f := newFixture(t)
	set := storage.DefaultSettings()
	set.ExportComments, set.ExportVideo, set.ExportPhoto = false, false, false
	if err := f.st.SaveSettings(context.Background(), 1, set); err != nil {
		t.Fatal(err)
	}
	_, read := f.run(t)
	p1 := read("posts/1.html")
	if strings.Contains(p1, "Comments (") || strings.Contains(p1, "question?") {
		t.Error("comments must be hidden")
	}
	if strings.Contains(p1, "<video") {
		t.Error("videos must be hidden")
	}
	if strings.Contains(read("posts/2.html"), "<img") {
		t.Error("photos must be hidden")
	}
}

func TestLessonWithNothingToShowIsDropped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// A post that only had a video; with videos hidden there is nothing left to show.
	_ = f.st.UpsertPost(ctx, storage.Post{ChannelID: 1, ID: 9, Date: 4000})
	_ = f.st.AddMedia(ctx, storage.Media{OwnerKind: "post", ChannelID: 1, OwnerID: 9, TGMediaID: 90, Kind: "video", FileName: "v.mp4"})
	list, _ := f.st.PendingMedia(ctx, 1)
	for _, m := range list {
		if m.TGMediaID == 90 {
			_ = f.st.SetMediaDone(ctx, m.ID, "media/post-9-90-v.mp4")
		}
	}
	set := storage.DefaultSettings()
	set.ExportVideo = false
	_ = f.st.SaveSettings(ctx, 1, set)
	idx, _ := f.run(t)
	b, _ := os.ReadFile(idx)
	if strings.Contains(string(b), "posts/9.html") {
		t.Error("empty lesson listed")
	}
}

func TestReExportReplacesSite(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	stale := filepath.Join(f.dir, "site", "posts", "stale.html")
	_ = os.WriteFile(stale, []byte("x"), 0o644)
	f.run(t)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("old files must not survive a re-export")
	}
}
