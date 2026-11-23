package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
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
	Channel   telegram.Channel
	Set       storage.Settings
	SinceDate string
	Notice    string
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

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.channel(w, r)
	if !ok {
		return
	}
	set, err := parseSettings(r)
	if err != nil {
		http.Error(w, "bad date: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.store.SaveSettings(r.Context(), ch.ID, set); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/channel/"+strconv.FormatInt(ch.ID, 10), http.StatusSeeOther)
}

func (s *Server) startSync(w http.ResponseWriter, r *http.Request) {
	s.startJob(w, r, "sync", true)
}

func (s *Server) startDownload(w http.ResponseWriter, r *http.Request) {
	s.startJob(w, r, "download", false)
}

// startJob runs sync (posts, comments, then media) or just the media download for a channel.
func (s *Server) startJob(w http.ResponseWriter, r *http.Request, kind string, withSync bool) {
	ch, ok := s.channel(w, r)
	if !ok {
		return
	}
	api, ok := s.tg.API()
	if !ok {
		http.Error(w, "not connected", http.StatusServiceUnavailable)
		return
	}
	opt := syncer.Options{IOwnAccess: r.FormValue("i_own_access") != ""}
	err := syncer.CheckAccess(ch, opt)
	if err == nil {
		src := telegram.Source{API: api}
		dir := storage.ChannelDir(s.cfg.DataDir, ch.ID, ch.Username)
		err = s.jobs.start(kind+" "+ch.Title, func(ctx context.Context, logf func(string, ...any)) error {
			if withSync {
				if err := (&syncer.Syncer{Src: src, Store: s.store}).Run(ctx, ch, opt, logf); err != nil {
					return err
				}
			}
			return (&downloader.Downloader{Fetch: src, Store: s.store}).Run(ctx, ch, dir, logf)
		})
	}
	back := "/channel/" + strconv.FormatInt(ch.ID, 10)
	if err != nil {
		back += "?notice=" + url.QueryEscape(err.Error())
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// startExport renders the static site from what is already in the archive (no Telegram needed).
func (s *Server) startExport(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.channel(w, r)
	if !ok {
		return
	}
	dir := storage.ChannelDir(s.cfg.DataDir, ch.ID, ch.Username)
	err := s.jobs.start("export "+ch.Title, func(ctx context.Context, logf func(string, ...any)) error {
		index, err := export.Run(ctx, s.store, ch.ID, ch.Title, dir, logf)
		if err == nil {
			abs, _ := filepath.Abs(index)
			logf("open file://%s", abs)
		}
		return err
	})
	back := "/channel/" + strconv.FormatInt(ch.ID, 10)
	if err != nil {
		back += "?notice=" + url.QueryEscape(err.Error())
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
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
