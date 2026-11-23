package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type Channel struct {
	ID, AccessHash, DiscussionID int64 // DiscussionID 0 = unknown / none
	Title, Username              string
	NoForwards                   bool
}

// UpsertChannel stores channel info; a zero DiscussionID never overwrites a known one.
func (s *Store) UpsertChannel(ctx context.Context, c Channel) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO channels (id, access_hash, title, username, discussion_id, noforwards)
VALUES (?, ?, ?, ?, NULLIF(?, 0), ?)
ON CONFLICT (id) DO UPDATE SET access_hash = excluded.access_hash, title = excluded.title,
  username = excluded.username, noforwards = excluded.noforwards,
  discussion_id = COALESCE(excluded.discussion_id, discussion_id)`,
		c.ID, c.AccessHash, c.Title, c.Username, c.DiscussionID, c.NoForwards)
	return err
}

// Settings are the per-channel sync options edited in the UI.
type Settings struct {
	SinceDays       int   // keep posts from the last N days; 0 = no limit
	SinceUnix       int64 // keep posts from this moment on; 0 = no limit
	IncludeComments bool
	DLVideo         bool
	DLDocument      bool
	DLPhoto         bool
	ExtAllow        string // comma-separated document extensions; "" = any
	MaxSizeMB       int    // 0 = unlimited
	Concurrency     int

	// Export options: what the generated site shows (independent of what was downloaded).
	ExportComments bool
	ExportVideo    bool
	ExportDocument bool
	ExportPhoto    bool
}

// AllowsExport reports whether files of this kind are shown in the exported site.
func (s Settings) AllowsExport(kind string) bool {
	switch kind {
	case "video":
		return s.ExportVideo
	case "photo":
		return s.ExportPhoto
	}
	return s.ExportDocument
}

// DefaultSettings mirrors the column defaults in 001_init.sql.
func DefaultSettings() Settings {
	return Settings{IncludeComments: true, DLVideo: true, DLDocument: true, DLPhoto: true, Concurrency: 3,
		ExportComments: true, ExportVideo: true, ExportDocument: true, ExportPhoto: true}
}

// Cutoff returns the oldest post date to sync (the later of the two limits); zero = no limit.
func (s Settings) Cutoff(now time.Time) time.Time {
	var t time.Time
	if s.SinceDays > 0 {
		t = now.AddDate(0, 0, -s.SinceDays)
	}
	if u := time.Unix(s.SinceUnix, 0); s.SinceUnix > 0 && u.After(t) {
		t = u
	}
	return t
}

// AllowsMedia reports whether a file passes the kind, extension and size filters.
func (s Settings) AllowsMedia(kind, fileName string, size int64) bool {
	switch kind {
	case "video":
		if !s.DLVideo {
			return false
		}
	case "photo":
		if !s.DLPhoto {
			return false
		}
	default:
		if !s.DLDocument {
			return false
		}
		if s.ExtAllow != "" {
			ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(fileName)), ".")
			ok := false
			for _, a := range strings.Split(strings.ToLower(s.ExtAllow), ",") {
				if strings.TrimPrefix(strings.TrimSpace(a), ".") == ext {
					ok = true
				}
			}
			if !ok {
				return false
			}
		}
	}
	return s.MaxSizeMB <= 0 || size <= int64(s.MaxSizeMB)<<20
}

// Settings returns the saved settings, or defaults if none were saved yet.
func (s *Store) Settings(ctx context.Context, channelID int64) (Settings, error) {
	var v Settings
	err := s.db.QueryRowContext(ctx, `
SELECT since_days, since_unix, include_comments, dl_video, dl_document, dl_photo, ext_allow, max_size_mb, concurrency,
  export_comments, export_video, export_document, export_photo
FROM channel_settings WHERE channel_id = ?`, channelID).
		Scan(&v.SinceDays, &v.SinceUnix, &v.IncludeComments, &v.DLVideo, &v.DLDocument, &v.DLPhoto,
			&v.ExtAllow, &v.MaxSizeMB, &v.Concurrency,
			&v.ExportComments, &v.ExportVideo, &v.ExportDocument, &v.ExportPhoto)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultSettings(), nil
	}
	return v, err
}

func (s *Store) SaveSettings(ctx context.Context, channelID int64, v Settings) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO channel_settings (channel_id, since_days, since_unix, include_comments, dl_video, dl_document,
  dl_photo, ext_allow, max_size_mb, concurrency, export_comments, export_video, export_document, export_photo)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (channel_id) DO UPDATE SET since_days = excluded.since_days, since_unix = excluded.since_unix,
  include_comments = excluded.include_comments, dl_video = excluded.dl_video, dl_document = excluded.dl_document,
  dl_photo = excluded.dl_photo, ext_allow = excluded.ext_allow, max_size_mb = excluded.max_size_mb,
  concurrency = excluded.concurrency, export_comments = excluded.export_comments,
  export_video = excluded.export_video, export_document = excluded.export_document,
  export_photo = excluded.export_photo`,
		channelID, v.SinceDays, v.SinceUnix, v.IncludeComments, v.DLVideo, v.DLDocument, v.DLPhoto,
		v.ExtAllow, v.MaxSizeMB, v.Concurrency,
		v.ExportComments, v.ExportVideo, v.ExportDocument, v.ExportPhoto)
	return err
}

type User struct {
	ID       int64
	Name     string
	Username string
}

func (s *Store) UpsertUser(ctx context.Context, u User) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO users (id, name, username) VALUES (?, ?, ?)
ON CONFLICT (id) DO UPDATE SET name = excluded.name, username = excluded.username`,
		u.ID, u.Name, u.Username)
	return err
}

// Post and Comment carry Entities as raw JSON; zero GroupedID/EditDate/AuthorID/ReplyToID are stored as NULL.
type Post struct {
	ChannelID, ID, Date, GroupedID, EditDate int64
	Text, Entities                           string
	RepliesCount                             int
}

func (s *Store) UpsertPost(ctx context.Context, p Post) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO posts (channel_id, id, date, text, entities, grouped_id, replies_count, edit_date)
VALUES (?, ?, ?, ?, ?, NULLIF(?, 0), ?, NULLIF(?, 0))
ON CONFLICT (channel_id, id) DO UPDATE SET date = excluded.date, text = excluded.text,
  entities = excluded.entities, grouped_id = excluded.grouped_id,
  replies_count = excluded.replies_count, edit_date = excluded.edit_date`,
		p.ChannelID, p.ID, p.Date, p.Text, jsonOrEmpty(p.Entities), p.GroupedID, p.RepliesCount, p.EditDate)
	return err
}

type Comment struct {
	ChannelID, ID, PostID, AuthorID, Date, ReplyToID int64
	Text, Entities                                   string
}

func (s *Store) UpsertComment(ctx context.Context, c Comment) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO comments (channel_id, id, post_id, author_id, date, text, entities, reply_to_id)
VALUES (?, ?, ?, NULLIF(?, 0), ?, ?, ?, NULLIF(?, 0))
ON CONFLICT (channel_id, id) DO UPDATE SET post_id = excluded.post_id, author_id = excluded.author_id,
  date = excluded.date, text = excluded.text, entities = excluded.entities, reply_to_id = excluded.reply_to_id`,
		c.ChannelID, c.ID, c.PostID, c.AuthorID, c.Date, c.Text, jsonOrEmpty(c.Entities), c.ReplyToID)
	return err
}

func jsonOrEmpty(s string) string {
	if s == "" {
		return "[]"
	}
	return s
}

type Media struct {
	ID                  int64  // row id, set by the store
	OwnerKind           string // "post" | "comment"
	ChannelID, OwnerID  int64
	PostID              int64 // set by PendingMedia: the post itself, or the post a comment belongs to
	TGMediaID           int64
	Kind                string // "video" | "document" | "photo"
	FileName, Mime      string
	Size                int64
	Duration            int
	Path, Status, Error string
}

// AddMedia registers a file as pending; an already known file keeps its status and path.
func (s *Store) AddMedia(ctx context.Context, m Media) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO media (owner_kind, channel_id, owner_id, tg_media_id, kind, file_name, mime, size, duration)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, 0))
ON CONFLICT (owner_kind, channel_id, owner_id, tg_media_id) DO NOTHING`,
		m.OwnerKind, m.ChannelID, m.OwnerID, m.TGMediaID, m.Kind, m.FileName, m.Mime, m.Size, m.Duration)
	return err
}

// PendingMedia lists files still to download (failed ones are retried on the next run).
func (s *Store) PendingMedia(ctx context.Context, channelID int64) ([]Media, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT m.id, m.owner_kind, m.channel_id, m.owner_id, COALESCE(c.post_id, m.owner_id), m.tg_media_id,
  m.kind, m.file_name, m.mime, m.size, COALESCE(m.duration, 0)
FROM media m LEFT JOIN comments c
  ON m.owner_kind = 'comment' AND c.channel_id = m.channel_id AND c.id = m.owner_id
WHERE m.channel_id = ? AND m.status IN ('pending', 'failed') ORDER BY m.id`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Media
	for rows.Next() {
		var m Media
		if err := rows.Scan(&m.ID, &m.OwnerKind, &m.ChannelID, &m.OwnerID, &m.PostID, &m.TGMediaID, &m.Kind,
			&m.FileName, &m.Mime, &m.Size, &m.Duration); err != nil {
			return nil, err
		}
		m.Status = "pending"
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) SetMediaDone(ctx context.Context, id int64, path string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE media SET status = 'done', path = ?, error = NULL WHERE id = ?`, path, id)
	return err
}

func (s *Store) SetMediaFailed(ctx context.Context, id int64, msg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE media SET status = 'failed', error = ? WHERE id = ?`, msg, id)
	return err
}

// SyncState records how far a channel has been synced.
type SyncState struct {
	MaxPostID   int   // newest synced post id; 0 = never synced
	CoveredFrom int64 // history back to this unix time is present; 0 = complete history
}

func (s *Store) SyncState(ctx context.Context, channelID int64) (SyncState, error) {
	var st SyncState
	err := s.db.QueryRowContext(ctx, `SELECT max_post_id, covered_from FROM sync_state WHERE channel_id = ?`, channelID).
		Scan(&st.MaxPostID, &st.CoveredFrom)
	if errors.Is(err, sql.ErrNoRows) {
		return SyncState{}, nil
	}
	return st, err
}

func (s *Store) SetSyncState(ctx context.Context, channelID int64, st SyncState, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sync_state (channel_id, max_post_id, covered_from, last_sync) VALUES (?, ?, ?, ?)
ON CONFLICT (channel_id) DO UPDATE SET max_post_id = excluded.max_post_id,
  covered_from = excluded.covered_from, last_sync = excluded.last_sync`,
		channelID, st.MaxPostID, st.CoveredFrom, at.Unix())
	return err
}

// PostRef is what comment sync needs to know about a stored post.
type PostRef struct {
	ID             int64
	RepliesCount   int // latest known number of comments in Telegram
	CommentsSynced int // number of comments when the thread was last fetched
}

// PostsForComments lists posts dated sinceUnix or later (0 = all), newest first.
func (s *Store) PostsForComments(ctx context.Context, channelID, sinceUnix int64) ([]PostRef, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, replies_count, comments_synced FROM posts WHERE channel_id = ? AND date >= ? ORDER BY id DESC`,
		channelID, sinceUnix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PostRef
	for rows.Next() {
		var p PostRef
		if err := rows.Scan(&p.ID, &p.RepliesCount, &p.CommentsSynced); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) SetPostReplies(ctx context.Context, channelID, postID int64, n int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE posts SET replies_count = ? WHERE channel_id = ? AND id = ?`, n, channelID, postID)
	return err
}

func (s *Store) SetCommentsSynced(ctx context.Context, channelID, postID int64, n int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE posts SET comments_synced = ? WHERE channel_id = ? AND id = ?`, n, channelID, postID)
	return err
}

// MaxCommentID is the newest stored comment id of a post (0 if none).
func (s *Store) MaxCommentID(ctx context.Context, channelID, postID int64) (int64, error) {
	var id sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT max(id) FROM comments WHERE channel_id = ? AND post_id = ?`, channelID, postID).Scan(&id)
	return id.Int64, err
}

// ChannelDir is where a channel's files live: <dataDir>/<username or channel-<id>>.
func ChannelDir(dataDir string, id int64, username string) string {
	name := fmt.Sprintf("channel-%d", id)
	if u := strings.Map(safeRune, username); u != "" {
		name = u
	}
	return filepath.Join(dataDir, name)
}

func safeRune(r rune) rune {
	if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
		return r
	}
	return -1
}

// ListPosts returns all stored posts of the channel, oldest first.
func (s *Store) ListPosts(ctx context.Context, channelID int64) ([]Post, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, date, text, entities, COALESCE(grouped_id, 0), replies_count, COALESCE(edit_date, 0)
FROM posts WHERE channel_id = ? ORDER BY date, id`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Post
	for rows.Next() {
		p := Post{ChannelID: channelID}
		if err := rows.Scan(&p.ID, &p.Date, &p.Text, &p.Entities, &p.GroupedID, &p.RepliesCount, &p.EditDate); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CommentRow is a stored comment joined with its author.
type CommentRow struct {
	Comment
	AuthorName, AuthorUsername string
}

// ListComments returns all stored comments of the channel, oldest first.
func (s *Store) ListComments(ctx context.Context, channelID int64) ([]CommentRow, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT c.id, c.post_id, COALESCE(c.author_id, 0), c.date, c.text, c.entities, COALESCE(c.reply_to_id, 0),
  COALESCE(u.name, ''), COALESCE(u.username, '')
FROM comments c LEFT JOIN users u ON u.id = c.author_id
WHERE c.channel_id = ? ORDER BY c.date, c.id`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CommentRow
	for rows.Next() {
		r := CommentRow{Comment: Comment{ChannelID: channelID}}
		if err := rows.Scan(&r.ID, &r.PostID, &r.AuthorID, &r.Date, &r.Text, &r.Entities, &r.ReplyToID,
			&r.AuthorName, &r.AuthorUsername); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DoneMedia returns the downloaded files of the channel (Path is relative to the channel dir).
func (s *Store) DoneMedia(ctx context.Context, channelID int64) ([]Media, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT owner_kind, owner_id, tg_media_id, kind, file_name, mime, size, COALESCE(path, '')
FROM media WHERE channel_id = ? AND status = 'done' ORDER BY id`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Media
	for rows.Next() {
		m := Media{ChannelID: channelID, Status: "done"}
		if err := rows.Scan(&m.OwnerKind, &m.OwnerID, &m.TGMediaID, &m.Kind, &m.FileName, &m.Mime, &m.Size, &m.Path); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
