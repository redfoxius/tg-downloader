package telegram

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// User is a comment author.
type User struct {
	ID       int64
	Name     string
	Username string
}

// Comment is one message of a post's discussion thread.
type Comment struct {
	ID           int // message id in the discussion group
	Date         time.Time
	Text         string
	EntitiesJSON string
	Author       *User // nil when the sender is not a user (e.g. a channel posting as itself)
	ReplyToID    int   // parent comment id; 0 for a top-level comment
	Media        []Media
}

// FetchComments streams the discussion thread of a post (newest first), skipping ids <= minID.
// A post without a thread yields no comments and no error.
func (s Source) FetchComments(ctx context.Context, ch Channel, postID, minID int, fn func(Comment) error) error {
	peer := &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash}
	offset := 0
	for {
		res, err := s.API.MessagesGetReplies(ctx, &tg.MessagesGetRepliesRequest{
			Peer: peer, MsgID: postID, OffsetID: offset, Limit: 100, MinID: minID,
		})
		if tgerr.Is(err, "MSG_ID_INVALID") {
			return nil
		}
		if err != nil {
			return fmt.Errorf("get replies of post %d: %w", postID, err)
		}
		mod, ok := res.AsModified()
		if !ok {
			return fmt.Errorf("unexpected replies response %T", res)
		}
		msgs := mod.GetMessages()
		if len(msgs) == 0 {
			return nil
		}
		users := usersByID(mod.GetUsers())
		for _, m := range msgs {
			if msg, ok := m.(*tg.Message); ok {
				if err := fn(commentFrom(msg, users)); err != nil {
					return err
				}
			}
		}
		offset = msgs[len(msgs)-1].GetID()
	}
}

func usersByID(list []tg.UserClass) map[int64]*User {
	out := make(map[int64]*User, len(list))
	for _, uc := range list {
		if u, ok := uc.AsNotEmpty(); ok {
			out[u.ID] = &User{ID: u.ID, Name: strings.TrimSpace(u.FirstName + " " + u.LastName), Username: u.Username}
		}
	}
	return out
}

func commentFrom(m *tg.Message, users map[int64]*User) Comment {
	c := Comment{
		ID: m.ID, Date: time.Unix(int64(m.Date), 0), Text: m.Message,
		EntitiesJSON: entitiesJSON(m.Entities), Media: mediaFrom(m.Media),
	}
	if p, ok := m.FromID.(*tg.PeerUser); ok {
		c.Author = users[p.UserID]
		if c.Author == nil { // user missing from the response: keep the id at least
			c.Author = &User{ID: p.UserID}
		}
	}
	if h, ok := m.ReplyTo.(*tg.MessageReplyHeader); ok {
		c.ReplyToID = parentComment(h.ReplyToMsgID, h.ReplyToTopID)
	}
	return c
}

// parentComment maps Telegram's reply header to a parent comment id. In a discussion group the
// thread root is the forwarded post: a reply to it (msg == top, or no top set) is top-level.
func parentComment(replyToMsg, replyToTop int) int {
	if replyToTop == 0 || replyToMsg == replyToTop {
		return 0
	}
	return replyToMsg
}

// ReplyCounts returns the current number of comments for the given post ids (deleted posts and
// posts without a comments thread are omitted). ids is split into batches internally.
func (s Source) ReplyCounts(ctx context.Context, ch Channel, ids []int) (map[int]int, error) {
	out := make(map[int]int, len(ids))
	for len(ids) > 0 {
		n := min(len(ids), 100)
		req := make([]tg.InputMessageClass, n)
		for i, id := range ids[:n] {
			req[i] = &tg.InputMessageID{ID: id}
		}
		res, err := s.API.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash}, ID: req,
		})
		if err != nil {
			return nil, fmt.Errorf("get messages: %w", err)
		}
		mod, ok := res.AsModified()
		if !ok {
			return nil, fmt.Errorf("unexpected messages response %T", res)
		}
		for _, m := range mod.GetMessages() {
			if msg, ok := m.(*tg.Message); ok {
				if r, ok := msg.GetReplies(); ok && r.Comments {
					out[msg.ID] = r.Replies
				} else {
					out[msg.ID] = 0
				}
			}
		}
		ids = ids[n:]
	}
	return out, nil
}
