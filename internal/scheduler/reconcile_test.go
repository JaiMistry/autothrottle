package scheduler

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/settings"
)

// The drift case reconciliation exists for: a session that started via
// the event stream and whose "stopped" notification never arrived,
// because the WebSocket dropped. A snapshot that no longer lists it
// has to clear it, or the downloader stays throttled forever.
func TestReplaceSessionsClearsSessionsTheEventStreamMissed(t *testing.T) {
	s, database, limiter := newTestScheduler(t)
	msID, dlID := addServerAndDownloader(t, database)
	if _, err := database.CreateMapping(msID, dlID); err != nil {
		t.Fatalf("creating mapping: %v", err)
	}

	s.HandleSessionEvent(msID, clients.Session{Key: "session-1"}, true)
	s.resolve(context.Background())
	if got := limiter.snapshot(); len(got) == 0 || got[len(got)-1].download != 500 {
		t.Fatalf("expected the session to throttle first, got %+v", got)
	}

	// Plex now reports nothing playing.
	s.ReplaceSessions(msID, nil)
	if n := s.ActiveSessionCount(msID); n != 0 {
		t.Fatalf("ActiveSessionCount = %d, want 0 after the snapshot cleared it", n)
	}

	s.resolve(context.Background())
	got := limiter.snapshot()
	if last := got[len(got)-1]; last.download != 0 || last.upld != 0 {
		t.Errorf("expected default rates restored, got %d/%d", last.download, last.upld)
	}
}

// The mirror case: a session that started while the connection was
// down, so no event ever announced it. The snapshot has to add it.
func TestReplaceSessionsAddsSessionsTheEventStreamMissed(t *testing.T) {
	s, database, limiter := newTestScheduler(t)
	msID, dlID := addServerAndDownloader(t, database)
	if _, err := database.CreateMapping(msID, dlID); err != nil {
		t.Fatalf("creating mapping: %v", err)
	}

	s.ReplaceSessions(msID, sessionsFrom("session-1"))
	if n := s.ActiveSessionCount(msID); n != 1 {
		t.Fatalf("ActiveSessionCount = %d, want 1", n)
	}

	s.resolve(context.Background())
	got := limiter.snapshot()
	if len(got) == 0 {
		t.Fatal("expected a rate-limit call")
	}
	if last := got[len(got)-1]; last.download != 500 || last.upld != 100 {
		t.Errorf("expected backup rates 500/100, got %d/%d", last.download, last.upld)
	}
}

// A snapshot matching what's already tracked must not re-resolve.
// Reconciliation runs on a timer for every server, so a no-op poll
// firing a downloader API call would mean constant pointless traffic.
func TestReplaceSessionsWithNoChangeDoesNotReapply(t *testing.T) {
	s, database, limiter := newTestScheduler(t)
	msID, dlID := addServerAndDownloader(t, database)
	if _, err := database.CreateMapping(msID, dlID); err != nil {
		t.Fatalf("creating mapping: %v", err)
	}

	s.HandleSessionEvent(msID, clients.Session{Key: "session-1"}, true)
	s.resolve(context.Background())
	before := len(limiter.snapshot())

	s.ReplaceSessions(msID, sessionsFrom("session-1"))
	s.resolve(context.Background())

	if after := len(limiter.snapshot()); after != before {
		t.Errorf("expected no additional rate-limit calls, got %d then %d", before, after)
	}
}

// Pausing has to survive a restart: someone who paused throttling
// before rebooting the host should not come back to it re-enabled.
func TestPauseStateIsRestoredFromSettings(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	defer func() { _ = database.Close() }()

	store := settings.New(database)
	first := New(database, make([]byte, 32), store)
	first.limiterFor = func(string) clients.RateLimiter { return &fakeLimiter{} }
	first.SetPaused(context.Background(), true)

	// A fresh scheduler over the same database stands in for a restart.
	second := New(database, make([]byte, 32), store)
	if !second.Paused() {
		t.Fatal("expected pause state to be restored from the settings table")
	}

	second.limiterFor = func(string) clients.RateLimiter { return &fakeLimiter{} }
	second.SetPaused(context.Background(), false)

	third := New(database, make([]byte, 32), store)
	if third.Paused() {
		t.Fatal("expected unpause to be persisted too")
	}
}
