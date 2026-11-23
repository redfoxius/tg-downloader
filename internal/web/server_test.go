package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalOnly(t *testing.T) {
	h := localOnly(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	cases := []struct {
		host, origin string
		want         int
	}{
		{"127.0.0.1:8181", "", 200},
		{"localhost:8181", "http://localhost:8181", 200},
		{"[::1]:8181", "", 200},
		{"evil.com", "", 403},                      // DNS rebinding
		{"127.0.0.1:8181", "http://evil.com", 403}, // cross-site POST
		{"127.0.0.1.evil.com:8181", "", 403},       // lookalike host
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/", nil)
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s origin=%q: got %d want %d", c.host, c.origin, w.Code, c.want)
		}
	}
}
