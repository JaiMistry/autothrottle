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
	"github.com/JaiMistry/autothrottle/internal/version"
	"github.com/JaiMistry/autothrottle/internal/watcher"
	"github.com/JaiMistry/autothrottle/internal/web"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// Everything AutoThrottle keeps lives in /data: the database and the
	// key that encrypts stored credentials. Fixed rather than
	// configurable — where it lives on the host is the bind mount's
	// business, so a second setting for the same thing would only be a
	// way for the two to disagree.
	const dataDir = "/data"
	if err := dropPrivileges(dataDir); err != nil {
		logger.Error("switching to PUID/PGID", "error", err)
		os.Exit(1)
	}
	if err := checkWritable(dataDir); err != nil {
		logger.Error("data directory is not usable", "dir", dataDir, "error", err)
		os.Exit(1)
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

	// Auth reads the database for an account created on the setup
	// screen, so it loads after the database rather than first.
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

	// The banner comes after the database is open so it can report what
	// was actually found, and before the watchers start so the log reads
	// in the order things happened.
	// Fixed, like the data path: which port to use on the host is the
	// port mapping's business. 7676 rather than 8080 because 8080 is
	// qBittorrent's default — the app most likely to be running beside
	// this one.
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
	samples := sampler.New(database, encryptionKey, sched)
	maintenance := retention.New(database)

	srv, err := web.New(database, authenticator, encryptionKey, sched, watchers)
	if err != nil {
		logger.Error("initializing web server", "error", err)
		os.Exit(1)
	}

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

	// Resolve once at startup, after a first snapshot has said what is
	// actually playing.
	//
	// Without this the app sits in "state not resolved yet" until some
	// session event happens to arrive, which on a quiet evening can be
	// hours: every downloader reads Unknown, and — worse than the
	// display — a client left throttled by the previous run stays
	// throttled, because nothing re-asserts the default rate. Snapshot
	// first so a stream that is already playing is seen before rates are
	// applied, rather than being briefly released and then re-throttled.
	go func() {
		watchers.SnapshotAll(ctx)
		if ctx.Err() != nil {
			return
		}
		sched.ResolveAsync()
	}()

	// Bandwidth sampling and housekeeping are independent of playback,
	// so they run on their own timers rather than off session events.
	go samples.Run(ctx)
	go maintenance.Run(ctx)

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

// countRows is for the banner only: a count that cannot be read is
// reported as zero rather than stopping startup, because a decorative
// line is never worth failing a boot over. Anything genuinely wrong
// with the database has already failed louder than this.
func countRows[T any](rows []T, err error) int {
	if err != nil {
		return 0
	}
	return len(rows)
}

// checkWritable fails early, and plainly, when the data directory can't
// be written. The usual cause is a bind mount the container's user does
// not own — Docker creates a missing host directory as root — and left
// to SQLite that surfaces as an opaque "unable to open database file".
func checkWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("%w — check the bind mount for /data", err)
	}
	probe, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return fmt.Errorf("cannot write to it as uid %d, gid %d — if the container is started with user:, that user must own the mounted directory; otherwise check the mount is not read-only", os.Getuid(), os.Getgid())
	}
	name := probe.Name()
	_ = probe.Close()
	return os.Remove(name)
}
