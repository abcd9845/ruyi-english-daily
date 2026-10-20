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
	"github.com/abcd9845/ruyi-english-daily/server/internal/attempts"
	"github.com/abcd9845/ruyi-english-daily/server/internal/auth"
	"github.com/abcd9845/ruyi-english-daily/server/internal/brain"
	"github.com/abcd9845/ruyi-english-daily/server/internal/config"
	"github.com/abcd9845/ruyi-english-daily/server/internal/embedded"
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

// brainIDOrDefault is the effective brain id for the startup summary (not a secret).
func brainIDOrDefault(id string) string {
	if id == "" {
		return brain.DefaultBrainID
	}
	return id
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	logw := server.NewRedactWriter(os.Stderr, cfg.Secrets())
	log := slog.New(slog.NewTextHandler(logw, &slog.HandlerOptions{Level: cfg.LogLevel}))
	for _, w := range cfg.Warnings {
		log.Warn("configuration warning", "detail", w)
	}

	entries, err := staticfiles.ParseAllowlist(ruyisite.StaticAllowlist)
	if err != nil {
		return err
	}
	static, missing := staticfiles.New(cfg.StaticDir, entries)
	for _, m := range missing {
		log.Warn("allowlisted file missing from static dir (will 404)", "file", m)
	}

	notifyTok := ""
	if cfg.QueueEnabled && cfg.QueueNotifyEnabled {
		notifyTok = cfg.BrainNotifyToken // the third token exists in the client only when the poller may run
	}
	bc, err := brain.New(brain.Config{BaseURL: cfg.BrainBaseURL, Token: cfg.BrainToken, GradeToken: cfg.BrainGradeToken, GradePath: cfg.BrainGradePath, LookupPath: cfg.BrainLookupPath, PickPath: cfg.BrainPickPath, BrainID: cfg.BrainID, InsecureTLS: cfg.BrainInsecure, CAFile: cfg.BrainCAFile,
		NotifyToken: notifyTok, PendingPath: cfg.BrainPendingPath})
	if err != nil {
		return err
	}
	if cfg.BrainInsecure {
		log.Warn("brain TLS verification is DISABLED (loopback address only)")
	}

	if cfg.Sets != nil {
		// One read-only probe of the brain's lookup endpoint (an obviously
		// non-existent qref; nothing is written). Informational only: it never
		// blocks or fails startup, and later requests degrade on their own.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			switch ok, perr := bc.ProbeLookup(ctx); {
			case perr != nil:
				log.Info("brain contract probe inconclusive (will degrade per request if needed)")
			case ok:
				log.Info("brain contract endpoints: lookup is deployed")
			default:
				log.Warn("brain contract endpoints not deployed yet (lookup 404/501): grading, picks and revisions degrade until the brain deploys them")
			}
		}()
	}

	emb, err := embedded.New(ruyisite.Pages(), ruyisite.KaTeX())
	if err != nil {
		return errors.New("embedded files unreadable")
	}
	if cfg.Sets != nil {
		log.Info("practice sets loaded", "sets", cfg.Sets.Len())
	}

	att := attempts.Open(cfg.AttemptsDir, log)
	defer att.Close()
	if cfg.QueueEnabled { // a queue without a usable state directory must not start
		if err := server.ProbeQueueDir(cfg.QueueStateDir); err != nil {
			return err
		}
	}

	deps := server.Deps{
		Auth:     auth.New(cfg.FamilyCode, cfg.SessionSecret, cfg.SessionTTL, cfg.CookieSecure, nil),
		Limiter:  server.NewLimiter(),
		Brain:    bc,
		Static:   static,
		Log:      log,
		Sources:  cfg.AllowedSource,
		Topic:    cfg.NtfyTopic,
		Sets:     cfg.Sets,
		Embedded: emb,

		Attempts:        att,
		NtfyBase:        cfg.NtfyBaseURL,
		NtfyRetryDelay:  cfg.NtfyRetryDelay,
		GradeMaxPerHour: cfg.GradeMaxPerHour,
		AutoSources:     cfg.AutoSources,
		SkipDeferred:    cfg.SkipDeferred,
		AutoTTL:         cfg.AutoTTL,

		QueueEnabled: cfg.QueueEnabled,
		QueueNotify:  cfg.QueueNotifyEnabled,
		QueueDir:     cfg.QueueStateDir,
		QueuePoll:    cfg.QueuePoll,
	}
	if cfg.QueueEnabled {
		deps.Queue = bc
		if cfg.AnswerImagesEnabled {
			deps.ImagesEnabled, deps.Media = true, bc
		}
	}
	app := server.New(deps)
	defer app.CloseQueue()
	app.StartQueue()
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
		"ntfy_configured", cfg.NtfyTopic != "", "grading_configured", cfg.BrainGradeToken != "",
		"queue_enabled", cfg.QueueEnabled, "queue_notify", cfg.QueueNotifyEnabled, "auto_sources", len(cfg.AutoSources), "grade_max_per_hour", cfg.GradeMaxPerHour, "brain_id", brainIDOrDefault(cfg.BrainID))

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
		// Parent notifications still in flight: one attempt takes at most 10 s
		// (server.ntfyAttemptTimeout), so wait up to 12 s; retries that are only
		// waiting for their turn are abandoned (logged), not sent early.
		dctx, dcancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer dcancel()
		if err := app.Drain(dctx); err != nil {
			log.Warn("ntfy sends still running at exit were dropped")
		}
	}
	log.Info("stopped")
	return nil
}
