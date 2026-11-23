package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/redfoxius/tg-downloader/internal/downloader"
	"github.com/redfoxius/tg-downloader/internal/export"
	"github.com/redfoxius/tg-downloader/internal/storage"
	"github.com/redfoxius/tg-downloader/internal/syncer"
	"github.com/redfoxius/tg-downloader/internal/telegram"
)

type channelView struct {
	Channel    telegram.Channel
	Set        storage.Settings
	SinceDate  string
	Notice     string
	SiteExists bool
}

// channel resolves {id} against the user's channel list (it carries the access hash).
func (s *Server) channel(w http.ResponseWriter, r *http.Request) (telegram.Channel, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err == nil {
		list, lerr := s.loadChannels(r.Context(), false)
		if lerr != nil {
			http.Error(w, lerr.Error(), http.StatusServiceUnavailable)
			return telegram.Channel{}, false
		}
		for _, c := range list {
			if c.ID == id {
				return c, true
			}
		}
	}
	http.NotFound(w, r)
	return telegram.Channel{}, false
}

func (s *Server) channelPage(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.channel(w, r)
	if !ok {
		return
	}
	set, err := s.store.Settings(r.Context(), ch.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	v := channelView{Channel: ch, Set: set, Notice: r.URL.Query().Get("notice")}
	index := filepath.Join(storage.ChannelDir(s.cfg.DataDir, ch.ID, ch.Username), "site", "index.html")
	_, statErr := os.Stat(index)
	v.SiteExists = statErr == nil
	if set.SinceUnix > 0 {
		v.SinceDate = time.Unix(set.SinceUnix, 0).Format("2006-01-02")
	}
	if err := tpl.ExecuteTemplate(w, "channel.html", v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func parseSettings(r *http.Request) (storage.Settings, error) {
	num := func(name string, def int) int {
		n, err := strconv.Atoi(strings.TrimSpace(r.FormValue(name)))
		if err != nil || n < 0 {
			return def
		}
		return n
	}
	set := storage.Settings{
		SinceDays:       num("since_days", 0),
		IncludeComments: r.FormValue("include_comments") != "",
		DLVideo:         r.FormValue("dl_video") != "",
		DLDocument:      r.FormValue("dl_document") != "",
		DLPhoto:         r.FormValue("dl_photo") != "",
		ExtAllow:        strings.TrimSpace(r.FormValue("ext_allow")),
		MaxSizeMB:       num("max_size_mb", 0),
		Concurrency:     min(max(num("concurrency", 3), 1), 16),
		ExportComments:  r.FormValue("export_comments") != "",
		ExportVideo:     r.FormValue("export_video") != "",
		ExportDocument:  r.FormValue("export_document") != "",
		ExportPhoto:     r.FormValue("export_photo") != "",
		AutoExport:      r.FormValue("auto_export") != "",
	}
	if d := r.FormValue("since_date"); d != "" {
		t, err := time.ParseInLocation("2006-01-02", d, time.Local)
		if err != nil {
			return set, err
		}
		set.SinceUnix = t.Unix()
	}
	return set, nil
}

// startArchive saves the settings from the form and runs the whole pipeline in one job:
// posts, comments, media, then (if enabled) the offline site.
func (s *Server) startArchive(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.channel(w, r)
	if !ok {
		return
	}
	back := "/channel/" + strconv.FormatInt(ch.ID, 10)
	fail := func(msg string) {
		http.Redirect(w, r, back+"?notice="+url.QueryEscape(msg), http.StatusSeeOther)
	}
	api, ok := s.tg.API()
	if !ok {
		fail("not connected to Telegram yet")
		return
	}
	set, err := parseSettings(r)
	if err != nil {
		fail("bad date: " + err.Error())
		return
	}
	opt := syncer.Options{IOwnAccess: r.FormValue("i_own_access") != ""}
	if err := syncer.CheckAccess(ch, opt); err != nil {
		fail(err.Error())
		return
	}
	if s.jobs.view().Running {
		fail(errBusy.Error())
		return
	}
	if err := s.store.SaveSettings(r.Context(), ch.ID, set); err != nil {
		fail(err.Error())
		return
	}

	src := telegram.Source{API: api}
	dir := storage.ChannelDir(s.cfg.DataDir, ch.ID, ch.Username)
	err = s.jobs.start(ch.Title, func(ctx context.Context, logf func(string, ...any)) error {
		logf("1/3 posts and comments")
		if err := (&syncer.Syncer{Src: src, Store: s.store}).Run(ctx, ch, opt, logf); err != nil {
			return err
		}
		logf("2/3 media files")
		if err := (&downloader.Downloader{Fetch: src, Store: s.store}).Run(ctx, ch, dir, logf); err != nil {
			return err
		}
		if !set.AutoExport {
			logf("done (site generation is off in the settings)")
			return nil
		}
		logf("3/3 offline site")
		if _, err := export.Run(ctx, s.store, ch.ID, ch.Title, dir, logf); err != nil {
			return err
		}
		logf("done: use \"Open site\" at the top of the page")
		return nil
	})
	if err != nil {
		fail(err.Error())
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// serveArchive exposes a channel folder (site/ and media/) so the site can be opened from the UI.
func (s *Server) serveArchive(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.channel(w, r)
	if !ok {
		return
	}
	dir := storage.ChannelDir(s.cfg.DataDir, ch.ID, ch.Username)
	http.StripPrefix("/archive/"+r.PathValue("id")+"/", http.FileServer(http.Dir(dir))).ServeHTTP(w, r)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	s.jobs.stop()
	back := r.Header.Get("Referer")
	if back == "" {
		back = "/"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) jobStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.jobs.view())
}
