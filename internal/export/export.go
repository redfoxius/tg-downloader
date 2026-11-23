// Package export renders the archive as a static, offline site (plain HTML/CSS, no external assets).
package export

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/redfoxius/tg-downloader/internal/storage"
)

//go:embed templates/*
var tplFS embed.FS

var (
	indexTpl  = template.Must(template.ParseFS(tplFS, "templates/index.html"))
	lessonTpl = template.Must(template.ParseFS(tplFS, "templates/lesson.html"))
)

type mediaItem struct {
	Kind, Src, Name, Size string
}

type node struct {
	ID       int64
	Author   string
	Username string
	Date     string
	Text     template.HTML
	Media    []mediaItem
	Children []*node
}

type lesson struct {
	ID           int64
	Title        string
	Date         string
	Text         template.HTML
	Media        []mediaItem
	Comments     []*node
	CommentCount int
	HasVideo     bool
}

// Run writes <channelDir>/site/{index.html,style.css,posts/<id>.html} and returns the index path.
// The site references downloaded files as ../../media/... so it works from file:// as long as
// the channel folder stays together. What is shown follows the channel's export settings.
func Run(ctx context.Context, st *storage.Store, channelID int64, title, channelDir string, logf func(string, ...any)) (string, error) {
	set, err := st.Settings(ctx, channelID)
	if err != nil {
		return "", err
	}
	lessons, err := build(ctx, st, channelID, set)
	if err != nil {
		return "", err
	}

	// Render into a temp dir and swap it in, so a failed export never leaves a half-written site.
	site, tmp := filepath.Join(channelDir, "site"), filepath.Join(channelDir, "site.tmp")
	if err := os.RemoveAll(tmp); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(tmp, "posts"), 0o755); err != nil {
		return "", err
	}
	css, _ := tplFS.ReadFile("templates/style.css")
	if err := os.WriteFile(filepath.Join(tmp, "style.css"), css, 0o644); err != nil {
		return "", err
	}
	if err := writePage(filepath.Join(tmp, "index.html"), indexTpl, "index.html", map[string]any{
		"Title": title, "Lessons": lessons, "Generated": time.Now().Format("2006-01-02 15:04"),
	}); err != nil {
		return "", err
	}
	for i, l := range lessons {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		data := map[string]any{"Site": title, "Lesson": l}
		if i > 0 {
			data["Prev"] = lessons[i-1]
		}
		if i < len(lessons)-1 {
			data["Next"] = lessons[i+1]
		}
		if err := writePage(filepath.Join(tmp, "posts", fmt.Sprintf("%d.html", l.ID)), lessonTpl, "lesson.html", data); err != nil {
			return "", err
		}
	}
	if err := os.RemoveAll(site); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, site); err != nil {
		return "", err
	}
	logf("exported %d posts to %s", len(lessons), site)
	return filepath.Join(site, "index.html"), nil
}

func writePage(path string, t *template.Template, name string, data any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := t.ExecuteTemplate(f, name, data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// build turns stored posts into lessons: album posts (same grouped_id) become one lesson, and
// lessons with nothing to show after the export filters are dropped.
func build(ctx context.Context, st *storage.Store, channelID int64, set storage.Settings) ([]*lesson, error) {
	posts, err := st.ListPosts(ctx, channelID)
	if err != nil {
		return nil, err
	}
	var comments []storage.CommentRow
	if set.ExportComments {
		if comments, err = st.ListComments(ctx, channelID); err != nil {
			return nil, err
		}
	}
	files, err := st.DoneMedia(ctx, channelID)
	if err != nil {
		return nil, err
	}

	mediaBy := mediaIndex{}
	for _, m := range files {
		if m.Path == "" || !set.AllowsExport(m.Kind) {
			continue
		}
		k := mediaKey{m.OwnerKind, m.OwnerID}
		mediaBy[k] = append(mediaBy[k], mediaItem{
			Kind: m.Kind, Name: m.FileName, Size: humanSize(m.Size),
			Src: (&url.URL{Path: "../../" + m.Path}).String(), // site/posts/<id>.html -> channel dir
		})
	}
	byPost := map[int64][]storage.CommentRow{}
	for _, c := range comments {
		byPost[c.PostID] = append(byPost[c.PostID], c)
	}

	cutoff := set.Cutoff(time.Now()) // the site follows the same depth as the sync
	var out []*lesson
	groups := map[int64]*lesson{} // grouped_id -> lesson
	for _, p := range posts {
		if !cutoff.IsZero() && time.Unix(p.Date, 0).Before(cutoff) {
			continue
		}
		var l *lesson
		if p.GroupedID != 0 {
			l = groups[p.GroupedID]
		}
		if l == nil {
			l = &lesson{ID: p.ID, Date: time.Unix(p.Date, 0).Format("2006-01-02 15:04")}
			out = append(out, l)
			if p.GroupedID != 0 {
				groups[p.GroupedID] = l
			}
		}
		if p.Text != "" {
			if l.Text != "" {
				l.Text += "<br><br>"
			}
			l.Text += renderText(p.Text, p.Entities)
			if l.Title == "" {
				l.Title = titleOf(p.Text)
			}
		}
		l.Media = append(l.Media, mediaBy[mediaKey{"post", p.ID}]...)
		l.Comments = append(l.Comments, commentTree(byPost[p.ID], mediaBy)...)
		l.CommentCount += len(byPost[p.ID])
	}

	kept := out[:0]
	for _, l := range out {
		if l.Text == "" && len(l.Media) == 0 {
			continue
		}
		if l.Title == "" {
			l.Title = fmt.Sprintf("Post #%d", l.ID)
		}
		for _, m := range l.Media {
			l.HasVideo = l.HasVideo || m.Kind == "video"
		}
		kept = append(kept, l)
	}
	return kept, nil
}

// mediaKey / mediaIndex: exportable files by owner ("post" or "comment" + message id).
type mediaKey struct {
	kind string
	id   int64
}

type mediaIndex map[mediaKey][]mediaItem

// commentTree nests comments by reply_to; replies whose parent is missing become top-level.
// rows are ordered oldest first, which keeps siblings in chronological order.
func commentTree(rows []storage.CommentRow, media mediaIndex) []*node {
	nodes := make(map[int64]*node, len(rows))
	for _, r := range rows {
		nodes[r.ID] = &node{
			ID: r.ID, Author: authorName(r), Username: r.AuthorUsername,
			Date:  time.Unix(r.Date, 0).Format("2006-01-02 15:04"),
			Text:  renderText(r.Text, r.Entities),
			Media: media[mediaKey{"comment", r.ID}],
		}
	}
	var roots []*node
	for _, r := range rows {
		n := nodes[r.ID]
		if parent, ok := nodes[r.ReplyToID]; ok && r.ReplyToID != r.ID {
			parent.Children = append(parent.Children, n)
		} else {
			roots = append(roots, n)
		}
	}
	return roots
}

func authorName(r storage.CommentRow) string {
	switch {
	case r.AuthorName != "":
		return r.AuthorName
	case r.AuthorUsername != "":
		return r.AuthorUsername
	}
	return "Unknown"
}

func titleOf(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if r := []rune(line); len(r) > 80 {
				return string(r[:80]) + "…"
			}
			return line
		}
	}
	return ""
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n > 0:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return ""
}
