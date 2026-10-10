package scheduler

import (
	"context"
	"testing"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/settings"
)

// A "stopped" event lost with a dropped socket: the snapshot must clear
// the session, or the downloader stays throttled forever.
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

// The mirror case: a session that started while the socket was down.
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

// The poll runs constantly, so an unchanged snapshot must not re-apply.
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

// A scheduler started over the same database restores the pause.
func TestPauseStateIsRestoredFromSettings(t *testing.T) {
	first, database, _ := newTestScheduler(t)
	first.SetPaused(context.Background(), true)
	second := New(database, make([]byte, 32), settings.New(database))
	if !second.Paused() {
		t.Fatal("expected pause state to be restored from the settings table")
	}
}
