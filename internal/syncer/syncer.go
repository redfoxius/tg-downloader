// Package syncer copies channel posts from Telegram into the archive database.
package syncer

import (
	"context"
	"errors"
	"time"

	"github.com/redfoxius/tg-downloader/internal/storage"
	"github.com/redfoxius/tg-downloader/internal/telegram"
)

// ErrNoForwards is returned for content-protected channels unless access is explicitly confirmed.
var ErrNoForwards = errors.New("channel has noforwards (content protection): pass the i-own-access confirmation to sync it")

// Source is what the syncer needs from Telegram; declared here, satisfied by telegram.Source.
type Source interface {
	FetchPosts(ctx context.Context, ch telegram.Channel, minID int, cutoff time.Time, fn func(telegram.Post) error) error
	FetchComments(ctx context.Context, ch telegram.Channel, postID, minID int, fn func(telegram.Comment) error) error
	ReplyCounts(ctx context.Context, ch telegram.Channel, ids []int) (map[int]int, error)
}

type Syncer struct {
	Src   Source
	Store *storage.Store
	Now   func() time.Time // defaults to time.Now
}

type Options struct {
	IOwnAccess bool // acknowledges syncing a noforwards channel
}

// CheckAccess refuses content-protected channels unless the user confirmed legal access.
// Every action that pulls content (sync, media download) must call it.
func CheckAccess(ch telegram.Channel, opt Options) error {
	if ch.NoForwards && !opt.IOwnAccess {
		return ErrNoForwards
	}
	return nil
}

// Run syncs posts, then (if enabled in the channel settings) their comments.
func (s *Syncer) Run(ctx context.Context, ch telegram.Channel, opt Options, logf func(string, ...any)) error {
	if err := s.SyncPosts(ctx, ch, opt, logf); err != nil {
		return err
	}
	return s.SyncComments(ctx, ch, logf)
}

// SyncPosts stores posts (and queues their media) newer than the last sync and within the
// channel's depth setting. Sync state is saved only after a complete pass, so an interrupted
// run is simply repeated (all writes are upserts).
func (s *Syncer) SyncPosts(ctx context.Context, ch telegram.Channel, opt Options, logf func(string, ...any)) error {
	if err := CheckAccess(ch, opt); err != nil {
		return err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	set, err := s.Store.Settings(ctx, ch.ID)
	if err != nil {
		return err
	}
	cutoff := set.Cutoff(now())
	st, err := s.Store.SyncState(ctx, ch.ID)
	if err != nil {
		return err
	}

	// Incremental only if the previous pass already covered the requested depth;
	// widening the depth forces a rescan back to the new cutoff.
	minID, coveredFrom := 0, unixOrZero(cutoff)
	if st.MaxPostID > 0 && (st.CoveredFrom == 0 || (!cutoff.IsZero() && cutoff.Unix() >= st.CoveredFrom)) {
		minID, coveredFrom = st.MaxPostID, st.CoveredFrom
	}

	dc := storage.Channel{ID: ch.ID, AccessHash: ch.AccessHash, Title: ch.Title, Username: ch.Username, NoForwards: ch.NoForwards}
	if err := s.Store.UpsertChannel(ctx, dc); err != nil {
		return err
	}

	maxID, posts, queued := st.MaxPostID, 0, 0
	discussionSaved := false
	err = s.Src.FetchPosts(ctx, ch, minID, cutoff, func(p telegram.Post) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.Store.UpsertPost(ctx, storage.Post{
			ChannelID: ch.ID, ID: int64(p.ID), Date: p.Date.Unix(), EditDate: unixOrZero(p.EditDate),
			Text: p.Text, Entities: p.EntitiesJSON, GroupedID: p.GroupedID, RepliesCount: p.RepliesCount,
		}); err != nil {
			return err
		}
		if p.DiscussionID != 0 && !discussionSaved {
			dc.DiscussionID = p.DiscussionID
			if err := s.Store.UpsertChannel(ctx, dc); err != nil {
				return err
			}
			discussionSaved = true
		}
		for _, m := range p.Media {
			if !set.AllowsMedia(m.Kind, m.FileName, m.Size) {
				continue
			}
			if err := s.Store.AddMedia(ctx, storage.Media{
				OwnerKind: "post", ChannelID: ch.ID, OwnerID: int64(p.ID), TGMediaID: m.TGID,
				Kind: m.Kind, FileName: m.FileName, Mime: m.Mime, Size: m.Size, Duration: m.Duration,
			}); err != nil {
				return err
			}
			queued++
		}
		posts++
		maxID = max(maxID, p.ID)
		if posts%100 == 0 {
			logf("%d posts…", posts)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := s.Store.SetSyncState(ctx, ch.ID, storage.SyncState{MaxPostID: maxID, CoveredFrom: coveredFrom}, now()); err != nil {
		return err
	}
	logf("done: %d posts, %d files queued", posts, queued)
	return nil
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// SyncComments fetches discussion threads of stored posts. Reply counts are refreshed from
// Telegram first, so new comments on old posts are noticed; only threads whose count changed
// are fetched, and only comments newer than the stored ones.
func (s *Syncer) SyncComments(ctx context.Context, ch telegram.Channel, logf func(string, ...any)) error {
	set, err := s.Store.Settings(ctx, ch.ID)
	if err != nil {
		return err
	}
	if !set.IncludeComments {
		return nil
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	refs, err := s.Store.PostsForComments(ctx, ch.ID, unixOrZero(set.Cutoff(now())))
	if err != nil {
		return err
	}
	ids := make([]int, len(refs))
	for i, r := range refs {
		ids[i] = int(r.ID)
	}
	counts, err := s.Src.ReplyCounts(ctx, ch, ids)
	if err != nil {
		return err
	}
	threads, total := 0, 0
	for _, r := range refs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if n, ok := counts[int(r.ID)]; ok && n != r.RepliesCount {
			if err := s.Store.SetPostReplies(ctx, ch.ID, r.ID, n); err != nil {
				return err
			}
			r.RepliesCount = n
		}
		if r.RepliesCount == r.CommentsSynced {
			continue
		}
		minID, err := s.Store.MaxCommentID(ctx, ch.ID, r.ID)
		if err != nil {
			return err
		}
		got := 0
		err = s.Src.FetchComments(ctx, ch, int(r.ID), int(minID), func(c telegram.Comment) error {
			got++
			return s.saveComment(ctx, ch.ID, r.ID, c, set)
		})
		if err != nil {
			return err
		}
		if err := s.Store.SetCommentsSynced(ctx, ch.ID, r.ID, r.RepliesCount); err != nil {
			return err
		}
		threads++
		total += got
		logf("post %d: +%d comments", r.ID, got)
	}
	logf("comments done: %d threads, %d new comments", threads, total)
	return nil
}

func (s *Syncer) saveComment(ctx context.Context, channelID, postID int64, c telegram.Comment, set storage.Settings) error {
	var authorID int64
	if c.Author != nil {
		authorID = c.Author.ID
		if err := s.Store.UpsertUser(ctx, storage.User{ID: c.Author.ID, Name: c.Author.Name, Username: c.Author.Username}); err != nil {
			return err
		}
	}
	if err := s.Store.UpsertComment(ctx, storage.Comment{
		ChannelID: channelID, ID: int64(c.ID), PostID: postID, AuthorID: authorID, Date: c.Date.Unix(),
		Text: c.Text, Entities: c.EntitiesJSON, ReplyToID: int64(c.ReplyToID),
	}); err != nil {
		return err
	}
	for _, m := range c.Media {
		if !set.AllowsMedia(m.Kind, m.FileName, m.Size) {
			continue
		}
		if err := s.Store.AddMedia(ctx, storage.Media{
			OwnerKind: "comment", ChannelID: channelID, OwnerID: int64(c.ID), TGMediaID: m.TGID,
			Kind: m.Kind, FileName: m.FileName, Mime: m.Mime, Size: m.Size, Duration: m.Duration,
		}); err != nil {
			return err
		}
	}
	return nil
}
