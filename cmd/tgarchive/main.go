// Command tgarchive archives Telegram channel posts, media and comments for offline use.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/redfoxius/tg-downloader/internal/config"
	"github.com/redfoxius/tg-downloader/internal/storage"
	"github.com/redfoxius/tg-downloader/internal/telegram"
	"github.com/redfoxius/tg-downloader/internal/web"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: tgarchive serve [--login qr|phone] [--config path] [--addr host:port]")
		os.Exit(2)
	}
	if err := serve(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultPath(), "config file")
	login := fs.String("login", "qr", "login method: qr or phone")
	addr := fs.String("addr", "", "listen address (default from config, 127.0.0.1:8181)")
	_ = fs.Parse(args)

	mode := telegram.Mode(*login)
	if mode != telegram.ModeQR && mode != telegram.ModePhone {
		return fmt.Errorf("--login must be qr or phone, got %q", *login)
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *addr != "" {
		cfg.Addr = *addr
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	store, err := storage.Open(filepath.Join(cfg.DataDir, "archive.db"))
	if err != nil {
		return err
	}
	defer store.Close()

	tg := telegram.New(mode, cfg.SessionPath, cfg.APIID, cfg.APIHash, cfg.RateLimit)
	go tg.Run(ctx)

	srv := &http.Server{Addr: cfg.Addr, Handler: web.New(ctx, tg, cfg, *cfgPath, store), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	fmt.Printf("open http://%s/ (login: %s, session: %s)\n", cfg.Addr, mode, cfg.SessionPath)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
