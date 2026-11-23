package export

import (
	"strings"
	"testing"
)

func TestRenderText(t *testing.T) {
	cases := []struct {
		name, text, ents, want string
	}{
		{"plain is escaped", "a <b> & c\nd", `[]`, "a &lt;b&gt; &amp; c<br>d"},
		{"bold", "hello world", `[{"type":"bold","offset":0,"length":5}]`, "<b>hello</b> world"},
		{"nested", "abc", `[{"type":"bold","offset":0,"length":3},{"type":"italic","offset":1,"length":1}]`, "<b>a<i>b</i>c</b>"},
		{"overlap stays well-formed", "abcd",
			`[{"type":"bold","offset":0,"length":3},{"type":"italic","offset":2,"length":2}]`,
			"<b>ab<i>c</i></b><i>d</i>"},
		{"utf16 offsets after emoji", "😀 hi", `[{"type":"bold","offset":3,"length":2}]`, "😀 <b>hi</b>"},
		{"text_url", "site", `[{"type":"text_url","offset":0,"length":4,"url":"https://e.com/?a=1&b=2"}]`,
			`<a href="https://e.com/?a=1&amp;b=2" target="_blank" rel="noopener noreferrer">site</a>`},
		{"javascript url is dropped", "x", `[{"type":"text_url","offset":0,"length":1,"url":"javascript:alert(1)"}]`, "x"},
		{"bare url gets https", "t.me/chan", `[{"type":"url","offset":0,"length":9}]`,
			`<a href="https://t.me/chan" target="_blank" rel="noopener noreferrer">t.me/chan</a>`},
		{"pre", "x=1", `[{"type":"pre","offset":0,"length":3}]`, "<pre><code>x=1</code></pre>"},
		{"out of range is clamped", "ab", `[{"type":"bold","offset":1,"length":99}]`, "a<b>b</b>"},
		{"bad json is plain", "a", `not json`, "a"},
	}
	for _, c := range cases {
		if got := string(renderText(c.text, c.ents)); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

func TestRenderTextNeverEmitsScript(t *testing.T) {
	got := string(renderText(`<script>alert(1)</script>`, `[{"type":"bold","offset":0,"length":8}]`))
	if strings.Contains(got, "<script") {
		t.Errorf("script tag leaked: %s", got)
	}
}
