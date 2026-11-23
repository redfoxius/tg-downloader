// Package web serves the local UI. It must only ever listen on loopback.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redfoxius/tg-downloader/internal/config"
	"github.com/redfoxius/tg-downloader/internal/storage"
	"github.com/redfoxius/tg-downloader/internal/telegram"
)

//go:embed templates/*.html
var tplFS embed.FS

var tpl = template.Must(template.ParseFS(tplFS, "templates/*.html"))

type Server struct {
	tg      *telegram.Client
	cfg     *config.Config
	cfgPath string
	store   *storage.Store
	jobs    *jobs

	mu       sync.Mutex
	channels []telegram.Channel // cached until refreshed
}

type view struct {
	telegram.Status
	Channels []telegram.Channel
	ListErr  string
}

// New builds the handler; ctx bounds background jobs (cancelled on shutdown).
func New(ctx context.Context, tg *telegram.Client, cfg *config.Config, cfgPath string, store *storage.Store) http.Handler {
	s := &Server{tg: tg, cfg: cfg, cfgPath: cfgPath, store: store, jobs: &jobs{base: ctx}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /qr.png", s.qr)
	mux.HandleFunc("POST /creds", s.creds)
	mux.HandleFunc("POST /submit", s.submit)
	mux.HandleFunc("POST /channels/refresh", s.refresh)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /channel/{id}", s.channelPage)
	mux.HandleFunc("POST /channel/{id}/settings", s.saveSettings)
	mux.HandleFunc("POST /channel/{id}/sync", s.startSync)
	mux.HandleFunc("POST /channel/{id}/download", s.startDownload)
	mux.HandleFunc("POST /channel/{id}/export", s.startExport)
	mux.HandleFunc("POST /job/cancel", s.cancelJob)
	mux.HandleFunc("GET /api/job", s.jobStatus)
	return localOnly(mux)
}

// localOnly rejects requests whose Host or Origin is not loopback (DNS rebinding / CSRF guard).
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r.Host) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			u, err := url.Parse(o)
			if err != nil || u.Host != r.Host {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopback(hostport string) bool {
	h, _, err := net.SplitHostPort(hostport)
	if err != nil {
		h = hostport
	}
	h = strings.Trim(h, "[]")
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	v := view{Status: s.tg.Status(), ListErr: r.URL.Query().Get("notice")}
	if v.State == telegram.StateAuthorized {
		var err error
		if v.Channels, err = s.loadChannels(r.Context(), false); err != nil {
			v.ListErr = err.Error()
		}
	}
	if err := tpl.ExecuteTemplate(w, "index.html", v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// loadChannels returns the cached list, fetching it on first use or when force is set.
func (s *Server) loadChannels(ctx context.Context, force bool) ([]telegram.Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channels != nil && !force {
		return s.channels, nil
	}
	api, ok := s.tg.API()
	if !ok {
		return nil, errors.New("not connected")
	}
	list, err := telegram.ListChannels(ctx, api)
	if err != nil {
		return nil, err
	}
	s.channels = list
	return list, nil
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	_, _ = s.loadChannels(r.Context(), true) // an error shows up on the next index render
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// logout signs the account out; refused while a job runs, since it would break mid-download.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if s.jobs.view().Running {
		http.Redirect(w, r, "/?notice="+url.QueryEscape("Cancel the running job before logging out."), http.StatusSeeOther)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.tg.Logout(ctx); err != nil {
		http.Redirect(w, r, "/?notice="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	s.mu.Lock()
	s.channels = nil // the next account has a different channel list
	s.mu.Unlock()
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.tg.Status())
}

func (s *Server) qr(w http.ResponseWriter, r *http.Request) {
	b, ok := s.tg.QRPNG()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func (s *Server) creds(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(strings.TrimSpace(r.FormValue("api_id")))
	hash := strings.TrimSpace(r.FormValue("api_hash"))
	if err != nil || id <= 0 || hash == "" {
		http.Error(w, "api_id must be a number and api_hash non-empty", http.StatusBadRequest)
		return
	}
	s.cfg.APIID, s.cfg.APIHash = id, hash
	if err := s.cfg.Save(s.cfgPath); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.tg.SetCreds(id, hash)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) submit(w http.ResponseWriter, r *http.Request) {
	if err := s.tg.Submit(telegram.State(r.FormValue("kind")), r.FormValue("value")); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
