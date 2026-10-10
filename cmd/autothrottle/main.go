// Command autothrottle throttles a torrent/download client's bandwidth
// while a media server is actively playing something.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	// The distroless image has no zoneinfo; without this TZ silently
	// falls back to UTC. Costs about 450 KB.
	_ "time/tzdata"

	"github.com/JaiMistry/autothrottle/internal/auth"
	"github.com/JaiMistry/autothrottle/internal/crypto"
	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/live"
	"github.com/JaiMistry/autothrottle/internal/retention"
	"github.com/JaiMistry/autothrottle/internal/scheduler"
	"github.com/JaiMistry/autothrottle/internal/settings"
	"github.com/JaiMistry/autothrottle/internal/version"
	"github.com/JaiMistry/autothrottle/internal/watcher"
	"github.com/JaiMistry/autothrottle/internal/web"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// Fixed: where it lives on the host is the bind mount's business.
	const dataDir = "/data"
	if err := dropPrivileges(dataDir); err != nil {
		logger.Error("switching to PUID/PGID", "error", err)
		os.Exit(1)
	}
	if err := checkWritable(dataDir); err != nil {
		logger.Error("data directory is not usable", "dir", dataDir, "error", err)
		os.Exit(1)
	}
	// Not fatal: a directory we can write but not chmod still works.
	if err := restrictDataDir(dataDir); err != nil {
		logger.Warn("could not restrict the data directory to its owner", "dir", dataDir, "error", err)
	}
	dbPath := filepath.Join(dataDir, "autothrottle.db")
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

	authenticator, err := auth.Load(database)
	if err != nil {
		logger.Error("loading auth config", "error", err)
		os.Exit(1)
	}

	encryptionKey, err := crypto.LoadOrGenerateKey(filepath.Join(dataDir, "encryption.key"))
	if err != nil {
		logger.Error("loading encryption key", "error", err)
		os.Exit(1)
	}

	// Before anything reads history: this closes what the previous run
	// left open at its last heartbeat, so downtime never reads as throttling.
	runID, err := database.StartRun(time.Now())
	if err != nil {
		logger.Error("recording this run", "error", err)
		os.Exit(1)
	}

	// Not 8080, which is qBittorrent's default.
	const addr = ":7676"
	printBanner(os.Stdout, version.Current(), startupInfo{
		DatabasePath: dbPath,
		Address:      addr,
		AuthMode:     authenticator.Mode(),
		AuthUser:     authenticator.Username(),
		MediaServers: countRows(database.ListMediaServers()),
		Downloaders:  countRows(database.ListDownloaders()),
		Mappings:     countRows(database.ListMappings()),
	})

	sched := scheduler.New(database, encryptionKey, settings.New(database))
	watchers := watcher.NewManager(database, encryptionKey, sched)
	maintenance := retention.New(database)

	srv, err := web.New(database, authenticator, encryptionKey, sched, watchers)
	if err != nil {
		logger.Error("initializing web server", "error", err)
		os.Exit(1)
	}

	hub := srv.Live()
	sched.SetNotifier(hub)
	watchers.SetNotifier(hub)
	database.OnEventsChanged(func() { hub.Notify(live.Logs) })

	httpServer := &http.Server{
		Addr:         addr,
		Handler:      srv.Router(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	// Open event streams never end on their own; without this, shutdown
	// waits out its whole timeout on them.
	httpServer.RegisterOnShutdown(hub.Close)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	watchers.Start(ctx)

	// Resolve once at startup, or a client left throttled by the previous
	// run stays throttled until some session event arrives. Snapshot first
	// so a stream already playing is not briefly released.
	go func() {
		watchers.SnapshotAll(ctx)
		if ctx.Err() != nil {
			return
		}
		sched.ResolveAsync()
	}()

	go maintenance.Run(ctx)
	go sched.RetryFailed(ctx)
	go heartbeat(ctx, database, runID)

	go func() {
		logger.Info("listening", "addr", addr, "auth_disabled", authenticator.Disabled())
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

// heartbeat keeps this run's last-seen time current, so the throttle
// history can tell downtime from hours in which nothing was throttled.
func heartbeat(ctx context.Context, database *db.DB, runID int64) {
	beat := func() {
		if err := database.Heartbeat(runID, time.Now()); err != nil {
			slog.Error("recording heartbeat", "error", err)
		}
	}
	for {
		select {
		case <-ctx.Done():
			beat()
			return
		case <-time.After(settings.HeartbeatInterval):
			beat()
		}
	}
}

// countRows is for the banner only: an unreadable count reads as zero
// rather than failing startup.
func countRows[T any](rows []T, err error) int {
	if err != nil {
		return 0
	}
	return len(rows)
}

// checkWritable fails early and plainly when the data directory can't be
// written (usually a bind mount Docker created as root), rather than as
// SQLite's opaque "unable to open database file".
func checkWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("%w (check the bind mount for /data)", err)
	}
	probe, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return fmt.Errorf("cannot write to it as uid %d, gid %d. If the container is started with user:, that user must own the mounted directory; otherwise check the mount is not read-only", os.Getuid(), os.Getgid())
	}
	name := probe.Name()
	_ = probe.Close()
	return os.Remove(name)
}
