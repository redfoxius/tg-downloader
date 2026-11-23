package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestChannelFrom(t *testing.T) {
	if _, ok := channelFrom(&tg.Channel{Megagroup: true}); ok {
		t.Error("supergroup must be skipped")
	}
	if _, ok := channelFrom(&tg.Channel{Broadcast: true, Left: true}); ok {
		t.Error("left channel must be skipped")
	}
	c, ok := channelFrom(&tg.Channel{ID: 5, Title: "W", Broadcast: true, HasLink: true, Noforwards: true})
	if !ok || c.ID != 5 || !c.HasDiscussion || !c.NoForwards {
		t.Errorf("got %+v ok=%v", c, ok)
	}
}
