package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/gotd/td/tg"
)

// Post is a channel post in the shape the archive needs.
type Post struct {
	ID           int
	Date         time.Time
	EditDate     time.Time // zero if never edited
	Text         string
	EntitiesJSON string
	GroupedID    int64
	RepliesCount int
	DiscussionID int64 // linked discussion group, when the post has a comments thread
	Media        []Media
}

// Media describes one downloadable file attached to a post.
type Media struct {
	TGID     int64
	Kind     string // video | document | photo
	FileName string
	Mime     string
	Size     int64
	Duration int // seconds
}

// Source fetches channel content over MTProto.
type Source struct{ API *tg.Client }

// FetchPosts streams posts newest-first, passing each to fn. It stops at the first post that is
// not newer than minID (when > 0) or is older than cutoff (when non-zero).
func (s Source) FetchPosts(ctx context.Context, ch Channel, minID int, cutoff time.Time, fn func(Post) error) error {
	peer := &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash}
	offset := 0
	for {
		res, err := s.API.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer: peer, OffsetID: offset, Limit: 100, MinID: minID,
		})
		if err != nil {
			return fmt.Errorf("get history: %w", err)
		}
		mod, ok := res.AsModified()
		if !ok {
			return fmt.Errorf("unexpected history response %T", res)
		}
		msgs := mod.GetMessages()
		if len(msgs) == 0 {
			return nil
		}
		for _, m := range msgs {
			if msg, ok := m.(*tg.Message); ok {
				if !cutoff.IsZero() && time.Unix(int64(msg.Date), 0).Before(cutoff) {
					return nil
				}
				if p, ok := postFrom(msg); ok {
					if err := fn(p); err != nil {
						return err
					}
				}
			}
		}
		offset = msgs[len(msgs)-1].GetID()
	}
}

// postFrom converts a message; posts with neither text nor media (polls, stickers...) are skipped.
func postFrom(m *tg.Message) (Post, bool) {
	p := Post{
		ID: m.ID, Date: time.Unix(int64(m.Date), 0), Text: m.Message,
		EntitiesJSON: entitiesJSON(m.Entities), GroupedID: m.GroupedID,
		Media: mediaFrom(m.Media),
	}
	if m.EditDate > 0 {
		p.EditDate = time.Unix(int64(m.EditDate), 0)
	}
	if r, ok := m.GetReplies(); ok && r.Comments {
		p.RepliesCount, p.DiscussionID = r.Replies, r.ChannelID
	}
	return p, p.Text != "" || len(p.Media) > 0
}

func mediaFrom(mm tg.MessageMediaClass) []Media {
	switch v := mm.(type) {
	case *tg.MessageMediaPhoto:
		if ph, ok := v.Photo.AsNotEmpty(); ok {
			return []Media{{TGID: ph.ID, Kind: "photo", Size: largestSize(ph.Sizes), Mime: "image/jpeg", FileName: fmt.Sprintf("%d.jpg", ph.ID)}}
		}
	case *tg.MessageMediaDocument:
		if d, ok := v.Document.AsNotEmpty(); ok {
			m := Media{TGID: d.ID, Kind: "document", Mime: d.MimeType, Size: d.Size}
			for _, a := range d.Attributes {
				switch a := a.(type) {
				case *tg.DocumentAttributeFilename:
					m.FileName = a.FileName
				case *tg.DocumentAttributeVideo:
					m.Kind, m.Duration = "video", int(a.Duration)
				}
			}
			if m.FileName == "" {
				m.FileName = fmt.Sprintf("%d%s", d.ID, extFromMime(d.MimeType))
			}
			return []Media{m}
		}
	}
	return nil
}

func extFromMime(mime string) string {
	switch mime {
	case "video/mp4":
		return ".mp4"
	case "application/pdf":
		return ".pdf"
	}
	if i := strings.IndexByte(mime, '/'); i >= 0 {
		return "." + path.Base(mime[i+1:])
	}
	return ""
}

func largestSize(sizes []tg.PhotoSizeClass) int64 {
	var max int64
	for _, s := range sizes {
		var n int64
		switch s := s.(type) {
		case *tg.PhotoSize:
			n = int64(s.Size)
		case *tg.PhotoSizeProgressive:
			for _, x := range s.Sizes {
				n = max64(n, int64(x))
			}
		}
		max = max64(max, n)
	}
	return max
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// Entity is the stored/exported form of a formatting entity.
type Entity struct {
	Type     string `json:"type"`
	Offset   int    `json:"offset"` // UTF-16 code units, as in Telegram
	Length   int    `json:"length"`
	URL      string `json:"url,omitempty"`
	Language string `json:"language,omitempty"`
	UserID   int64  `json:"user_id,omitempty"`
}

func entitiesJSON(es []tg.MessageEntityClass) string {
	out := make([]Entity, 0, len(es))
	for _, e := range es {
		x := Entity{Offset: e.GetOffset(), Length: e.GetLength()}
		switch e := e.(type) {
		case *tg.MessageEntityBold:
			x.Type = "bold"
		case *tg.MessageEntityItalic:
			x.Type = "italic"
		case *tg.MessageEntityUnderline:
			x.Type = "underline"
		case *tg.MessageEntityStrike:
			x.Type = "strike"
		case *tg.MessageEntityCode:
			x.Type = "code"
		case *tg.MessageEntityPre:
			x.Type, x.Language = "pre", e.Language
		case *tg.MessageEntityBlockquote:
			x.Type = "blockquote"
		case *tg.MessageEntitySpoiler:
			x.Type = "spoiler"
		case *tg.MessageEntityTextURL:
			x.Type, x.URL = "text_url", e.URL
		case *tg.MessageEntityURL:
			x.Type = "url"
		case *tg.MessageEntityEmail:
			x.Type = "email"
		case *tg.MessageEntityMention:
			x.Type = "mention"
		case *tg.MessageEntityHashtag:
			x.Type = "hashtag"
		case *tg.MessageEntityMentionName:
			x.Type, x.UserID = "mention_name", e.UserID
		default: // unsupported kinds (custom emoji, cashtags...) render as plain text
			continue
		}
		out = append(out, x)
	}
	b, _ := json.Marshal(out) // plain structs: cannot fail
	return string(b)
}
