// Command autothrottle throttles a torrent/download client's bandwidth
// while a media server is actively playing something.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
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

// run starts everything and serves on addr until ctx ends.
func run(ctx context.Context, logger *slog.Logger, dataDir, addr string, out io.Writer) error {
	if err := checkWritable(dataDir); err != nil {
		return fmt.Errorf("data directory %s is not usable: %w", dataDir, err)
	}
	// Not fatal: a directory we can write but not chmod still works.
	if err := restrictDataDir(dataDir); err != nil {
		logger.Warn("could not restrict the data directory to its owner", "dir", dataDir, "error", err)
	}
	dbPath := filepath.Join(dataDir, "autothrottle.db")
	database, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			logger.Error("closing database", "error", err)
		}
	}()

	authenticator, err := auth.Load(database)
	if err != nil {
		return fmt.Errorf("loading auth config: %w", err)
	}

	encryptionKey, err := crypto.LoadOrGenerateKey(filepath.Join(dataDir, "encryption.key"))
	if err != nil {
		return fmt.Errorf("loading encryption key: %w", err)
	}

	// Before anything reads history: this closes what the previous run
	// left open at its last heartbeat, so downtime never reads as throttling.
	runID, err := database.StartRun(time.Now())
	if err != nil {
		return fmt.Errorf("recording this run: %w", err)
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	printBanner(out, version.Current(), startupInfo{
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
		_ = listener.Close()
		return fmt.Errorf("initializing web server: %w", err)
	}
	if localStatusEnabled {
		srv.EnableLocalStatus()
	}

	hub := srv.Live()
	sched.SetNotifier(hub)
	watchers.SetNotifier(hub)
	database.OnEventsChanged(func() { hub.Notify(live.Logs) })

	httpServer := &http.Server{
		Handler:      srv.Router(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	// Open event streams never end on their own; without this, shutdown
	// waits out its whole timeout on them.
	httpServer.RegisterOnShutdown(hub.Close)

	// Ends the background work however run returns. The last heartbeat
	// is waited for: it records when this run stopped.
	ctx, cancel := context.WithCancel(ctx)
	beating := make(chan struct{})
	defer func() { <-beating }()
	defer cancel()

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
	go sched.RunClock(ctx)
	go sched.WatchClients(ctx)
	go func() {
		heartbeat(ctx, database, runID)
		close(beating)
	}()

	served := make(chan error, 1)
	go func() { served <- httpServer.Serve(listener) }()
	logger.Info("listening", "addr", listener.Addr().String(), "auth_disabled", authenticator.Disabled())

	select {
	case err := <-served:
		return fmt.Errorf("server failed: %w", err)
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	if releaseOnExit {
		releaseCtx, cancelRelease := context.WithTimeout(context.Background(), 5*time.Second)
		sched.Release(releaseCtx)
		cancelRelease()
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown failed: %w", err)
	}
	return nil
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
// written, rather than as SQLite's opaque "unable to open database file".
func checkWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("%w (%s)", err, dataDirHint)
	}
	probe, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return notWritable()
	}
	name := probe.Name()
	_ = probe.Close()
	return os.Remove(name)
}
