package export

import (
	"encoding/json"
	"html"
	"html/template"
	"net/url"
	"sort"
	"strings"
	"unicode/utf16"
)

// entity mirrors the JSON written by the sync step (offsets are UTF-16 code units).
type entity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	URL    string `json:"url"`
}

type span struct {
	start, end  int
	open, close string
}

// renderText turns Telegram text + entities into safe HTML. All text is escaped; links are
// limited to http(s), mailto and tg so stored content can never inject script.
func renderText(text, entitiesJSON string) template.HTML {
	u := utf16.Encode([]rune(text))
	var ents []entity
	_ = json.Unmarshal([]byte(entitiesJSON), &ents) // bad JSON degrades to plain text

	var spans []span
	for _, e := range ents {
		start, end := e.Offset, min(e.Offset+e.Length, len(u))
		if e.Length <= 0 || start < 0 || start >= len(u) {
			continue
		}
		if o, c, ok := tagsFor(e, string(utf16.Decode(u[start:end]))); ok {
			spans = append(spans, span{start, end, o, c})
		}
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end > spans[j].end // outer span first
	})

	pts := map[int]bool{len(u): true}
	for _, s := range spans {
		pts[s.start], pts[s.end] = true, true
	}
	bounds := make([]int, 0, len(pts))
	for p := range pts {
		bounds = append(bounds, p)
	}
	sort.Ints(bounds)

	var sb strings.Builder
	var stack []int // indexes into spans, outermost first
	pos := 0
	for _, b := range bounds {
		sb.WriteString(escape(string(utf16.Decode(u[pos:b]))))
		pos = b
		// Close spans ending here. If one sits under spans that continue, close those too
		// and reopen them, so the output stays well-formed for overlapping entities.
		for k := 0; k < len(stack); k++ {
			if spans[stack[k]].end != b {
				continue
			}
			var survivors []int
			for i := len(stack) - 1; i >= k; i-- {
				sb.WriteString(spans[stack[i]].close)
				if spans[stack[i]].end != b {
					survivors = append([]int{stack[i]}, survivors...)
				}
			}
			stack = stack[:k]
			for _, i := range survivors {
				sb.WriteString(spans[i].open)
				stack = append(stack, i)
			}
			break
		}
		for i, s := range spans {
			if s.start == b {
				sb.WriteString(s.open)
				stack = append(stack, i)
			}
		}
	}
	return template.HTML(sb.String())
}

func escape(s string) string {
	return strings.ReplaceAll(html.EscapeString(s), "\n", "<br>")
}

// tagsFor returns the opening and closing HTML for an entity; ok is false for unsupported
// entities and for links with unsafe targets (those render as plain text).
func tagsFor(e entity, covered string) (open, close string, ok bool) {
	switch e.Type {
	case "bold":
		return "<b>", "</b>", true
	case "italic":
		return "<i>", "</i>", true
	case "underline":
		return "<u>", "</u>", true
	case "strike":
		return "<s>", "</s>", true
	case "code":
		return "<code>", "</code>", true
	case "pre":
		return "<pre><code>", "</code></pre>", true
	case "blockquote":
		return "<blockquote>", "</blockquote>", true
	case "spoiler":
		return `<span class="spoiler">`, "</span>", true
	case "mention", "mention_name":
		return `<span class="mention">`, "</span>", true
	case "hashtag":
		return `<span class="hashtag">`, "</span>", true
	case "text_url":
		return link(e.URL)
	case "url":
		return link(strings.TrimSpace(covered))
	case "email":
		return link("mailto:" + strings.TrimSpace(covered))
	}
	return "", "", false
}

func link(raw string) (open, close string, ok bool) {
	if !strings.Contains(raw, ":") { // bare "t.me/x" style link
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "mailto", "tg":
	default:
		return "", "", false
	}
	return `<a href="` + html.EscapeString(u.String()) + `" target="_blank" rel="noopener noreferrer">`, "</a>", true
}
