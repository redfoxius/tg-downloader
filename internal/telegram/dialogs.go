package telegram

import (
	"context"

	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/tg"
)

// Channel is a broadcast channel the user is subscribed to.
type Channel struct {
	ID            int64
	AccessHash    int64
	Title         string
	Username      string
	HasDiscussion bool // linked discussion group exists (its id is resolved at sync time)
	NoForwards    bool // content protection: saving/forwarding is restricted
}

// channelFrom keeps only joined broadcast channels.
func channelFrom(ch *tg.Channel) (Channel, bool) {
	if !ch.Broadcast || ch.Left {
		return Channel{}, false
	}
	return Channel{
		ID: ch.ID, AccessHash: ch.AccessHash, Title: ch.Title, Username: ch.Username,
		HasDiscussion: ch.HasLink, NoForwards: ch.Noforwards,
	}, true
}

// ListChannels returns the user's broadcast channels, most recently active first.
func ListChannels(ctx context.Context, api *tg.Client) ([]Channel, error) {
	var out []Channel
	it := query.GetDialogs(api).BatchSize(100).Iter()
	for it.Next(ctx) {
		p, ok := it.Value().Peer.(*tg.InputPeerChannel)
		if !ok {
			continue
		}
		if ent, ok := it.Value().Entities.Channel(p.ChannelID); ok {
			if c, ok := channelFrom(ent); ok {
				out = append(out, c)
			}
		}
	}
	return out, it.Err()
}
