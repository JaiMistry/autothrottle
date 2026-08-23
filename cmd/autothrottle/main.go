// Command autothrottle throttles a torrent/download client's bandwidth
// while a media server is actively playing something.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	// The final image is distroless and carries no zoneinfo, so TZ would
	// silently fall back to UTC and every timestamp in the UI would be
	// wrong for everyone outside it. Embedding the database costs about
	// 450 KB and makes TZ work the way an operator expects.
	_ "time/tzdata"

	"github.com/JaiMistry/autothrottle/internal/auth"
	"github.com/JaiMistry/autothrottle/internal/crypto"
	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/retention"
	"github.com/JaiMistry/autothrottle/internal/sampler"
	"github.com/JaiMistry/autothrottle/internal/scheduler"
	"github.com/JaiMistry/autothrottle/internal/settings"
	"github.com/JaiMistry/autothrottle/internal/watcher"
	"github.com/JaiMistry/autothrottle/internal/web"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	authCfg, err := auth.LoadConfig()
	if err != nil {
		logger.Error("loading auth config", "error", err)
		os.Exit(1)
	}

	dbPath := envOr("AUTOTHROTTLE_DB_PATH", "/data/autothrottle.db")
	database, err := db.Open(dbPath)
	if err != nil {
		logger.Error("opening database", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := database.Close(); err != nil {
			logger.Error("closing database", "error", err)
		}
	}()

	keyPath := envOr("AUTOTHROTTLE_ENCRYPTION_KEY_PATH", filepath.Join(filepath.Dir(dbPath), "encryption.key"))
	encryptionKey, err := crypto.LoadOrGenerateKey(os.Getenv("AUTOTHROTTLE_ENCRYPTION_KEY"), keyPath)
	if err != nil {
		logger.Error("loading encryption key", "error", err)
		os.Exit(1)
	}

	// Sessions that were playing when the process last stopped are
	// still marked open. Nothing observed them ending, so close them
	// before anything reads history — otherwise they read as playing
	// right now, and every downloader they map to looks throttled for
	// the whole outage.
	if closed, err := database.CloseDanglingSessions(); err != nil {
		logger.Error("closing sessions left open by the previous run", "error", err)
	} else if closed > 0 {
		logger.Info("closed sessions left open by the previous run", "sessions", closed)
	}

	store := settings.New(database)
	sched := scheduler.New(database, encryptionKey, store)
	watchers := watcher.NewManager(database, encryptionKey, sched, store)
	samples := sampler.New(database, encryptionKey, store, sched)
	maintenance := retention.New(database, store)

	srv, err := web.New(database, authCfg, encryptionKey, sched, watchers, store)
	if err != nil {
		logger.Error("initializing web server", "error", err)
		os.Exit(1)
	}

	addr := envOr("LISTEN_ADDR", ":8080")
	httpServer := &http.Server{
		Addr:         addr,
		Handler:      srv.Router(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Watchers run for the lifetime of ctx. Configuration changes
	// reconcile this set live, from the web handlers — no restart.
	watchers.Start(ctx)

	// Bandwidth sampling and housekeeping are independent of playback,
	// so they run on their own timers rather than off session events.
	go samples.Run(ctx)
	go maintenance.Run(ctx)

	go func() {
		logger.Info("listening", "addr", addr, "auth_disabled", authCfg.Disabled)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
