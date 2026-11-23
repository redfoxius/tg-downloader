package web

import (
	"strings"
	"testing"

	"github.com/redfoxius/tg-downloader/internal/telegram"
)

func TestAuthorizedViewListsChannels(t *testing.T) {
	var b strings.Builder
	v := view{
		Status:   telegram.Status{State: telegram.StateAuthorized, User: "Me"},
		Channels: []telegram.Channel{{Title: "<Workshop>", HasDiscussion: true, NoForwards: true}},
	}
	if err := tpl.ExecuteTemplate(&b, "index.html", v); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"&lt;Workshop&gt;", "comments", "noforwards"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}
