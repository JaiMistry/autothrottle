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

	"github.com/JaiMistry/autothrottle/internal/auth"
	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/crypto"
	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/scheduler"
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

	sched := scheduler.New(database, encryptionKey)

	srv, err := web.New(database, authCfg, encryptionKey, sched)
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

	startWatchers(ctx, database, encryptionKey, sched, logger)

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

// startWatchers starts a Plex session watcher per enabled Plex media
// server, forwarding its events into the scheduler. Watchers run for
// the lifetime of ctx. Reconfiguring media servers via the
// Configuration page takes effect on next restart in v1 — hot add/
// remove of watchers is a natural v2 follow-up once the mapping UI has
// proven itself in real use.
func startWatchers(ctx context.Context, database *db.DB, encryptionKey []byte, sched *scheduler.Scheduler, logger *slog.Logger) {
	mediaServers, err := database.ListMediaServers()
	if err != nil {
		logger.Error("listing media servers for watchers", "error", err)
		return
	}
	for _, ms := range mediaServers {
		if !ms.Enabled || ms.Type != "plex" {
			continue
		}
		token, err := crypto.Decrypt(encryptionKey, ms.CredentialEncrypted)
		if err != nil {
			logger.Error("decrypting media server token", "media_server", ms.Name, "error", err)
			continue
		}

		watcher := clients.NewPlexWatcher(ms.URL, token, ms.SkipLocalPlayback)
		mediaServerID := ms.ID
		mediaServerName := ms.Name

		go watcher.Run(ctx)
		go func() {
			events := watcher.Events()
			for {
				select {
				case <-ctx.Done():
					return
				case event, ok := <-events:
					if !ok {
						return
					}
					sched.HandleSessionEvent(mediaServerID, event.SessionKey, event.Active)
				}
			}
		}()
		logger.Info("started plex watcher", "media_server", mediaServerName, "url", ms.URL)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
