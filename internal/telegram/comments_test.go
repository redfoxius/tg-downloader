package telegram

import "testing"

func TestParentComment(t *testing.T) {
	cases := []struct{ msg, top, want int }{
		{500, 0, 0},     // reply to the thread root, no top set
		{500, 500, 0},   // reply to the thread root
		{510, 500, 510}, // reply to another comment
	}
	for _, c := range cases {
		if got := parentComment(c.msg, c.top); got != c.want {
			t.Errorf("parentComment(%d,%d) = %d, want %d", c.msg, c.top, got, c.want)
		}
	}
}
