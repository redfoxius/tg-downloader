package web

import (
	"context"
	"github.com/redfoxius/tg-downloader/internal/telegram"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestParseSettings(t *testing.T) {
	form := url.Values{
		"since_days": {"30"}, "since_date": {"2026-01-15"}, "include_comments": {"on"}, "dl_video": {"on"},
		"ext_allow": {" pdf,zip "}, "max_size_mb": {"-5"}, "concurrency": {"99"}, "export_photo": {"on"},
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	set, err := parseSettings(r)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 1, 15, 0, 0, 0, 0, time.Local).Unix()
	switch {
	case set.SinceDays != 30, set.SinceUnix != want:
		t.Errorf("depth: %+v", set)
	case !set.IncludeComments, !set.DLVideo, set.DLDocument, set.DLPhoto:
		t.Errorf("checkboxes: %+v", set) // unchecked boxes are absent from the form
	case set.ExtAllow != "pdf,zip":
		t.Errorf("ext: %q", set.ExtAllow)
	case set.ExportComments, set.ExportVideo, set.ExportDocument, !set.ExportPhoto:
		t.Errorf("export checkboxes: %+v", set)
	case set.MaxSizeMB != 0, set.Concurrency != 16:
		t.Errorf("clamping: %+v", set)
	}
}

func TestLogoutRefusedWhileJobRuns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Server{tg: telegram.New(telegram.ModeQR, "unused", 1, "h", 5), jobs: &jobs{base: ctx}}
	_ = s.jobs.start("sync", func(ctx context.Context, _ func(string, ...any)) error { <-ctx.Done(); return nil })

	w := httptest.NewRecorder()
	s.logout(w, httptest.NewRequest("POST", "/logout", nil))
	if loc := w.Header().Get("Location"); w.Code != 303 || !strings.Contains(loc, "Cancel+the+running+job") {
		t.Errorf("code=%d location=%q", w.Code, loc)
	}

	s.jobs.stop() // idle and not signed in: the client itself refuses
	for i := 0; i < 100 && s.jobs.view().Running; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	w = httptest.NewRecorder()
	s.logout(w, httptest.NewRequest("POST", "/logout", nil))
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "not+signed+in") {
		t.Errorf("location=%q", loc)
	}
}
