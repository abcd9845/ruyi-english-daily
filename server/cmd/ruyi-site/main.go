// Command ruyi-site serves the practice site: a fixed allowlist of static
// files plus the family-code login and the read-only brain proxy.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	ruyisite "github.com/abcd9845/ruyi-english-daily/server"
	"github.com/abcd9845/ruyi-english-daily/server/internal/auth"
	"github.com/abcd9845/ruyi-english-daily/server/internal/brain"
	"github.com/abcd9845/ruyi-english-daily/server/internal/config"
	"github.com/abcd9845/ruyi-english-daily/server/internal/server"
	"github.com/abcd9845/ruyi-english-daily/server/internal/staticfiles"
)

func main() {
	if err := run(); err != nil {
		// err never contains configuration values (see package config).
		fmt.Fprintln(os.Stderr, "ruyi-site:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	logw := server.NewRedactWriter(os.Stderr, cfg.Secrets())
	log := slog.New(slog.NewTextHandler(logw, &slog.HandlerOptions{Level: cfg.LogLevel}))

	entries, err := staticfiles.ParseAllowlist(ruyisite.StaticAllowlist)
	if err != nil {
		return err
	}
	static, missing := staticfiles.New(cfg.StaticDir, entries)
	for _, m := range missing {
		log.Warn("allowlisted file missing from static dir (will 404)", "file", m)
	}

	bc, err := brain.New(brain.Config{BaseURL: cfg.BrainBaseURL, Token: cfg.BrainToken, InsecureTLS: cfg.BrainInsecure, CAFile: cfg.BrainCAFile})
	if err != nil {
		return err
	}
	if cfg.BrainInsecure {
		log.Warn("brain TLS verification is DISABLED (loopback address only)")
	}

	app := server.New(server.Deps{
		Auth:    auth.New(cfg.FamilyCode, cfg.SessionSecret, cfg.SessionTTL, cfg.CookieSecure, nil),
		Limiter: server.NewLimiter(),
		Brain:   bc,
		Static:  static,
		Log:     log,
		Sources: cfg.AllowedSource,
		Topic:   cfg.NtfyTopic,
	})
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           app,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      3 * time.Minute, // slow phones fetching the listening mp3
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", cfg.Listen, "static_files", len(entries), "cookie_secure", cfg.CookieSecure,
		"ntfy_configured", cfg.NtfyTopic != "")

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("listen failed")
		}
	case <-ctx.Done():
		log.Info("shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(sctx); err != nil {
			_ = srv.Close()
		}
	}
	log.Info("stopped")
	return nil
}
