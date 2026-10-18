package scheduler

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/settings"
)

// Detail for a session the event stream announced bare is merged in, and
// detail for one not playing is dropped.
func TestSessionDetailMerging(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	msID, _ := addServerAndDownloader(t, database)

	s.UpdateSessionDetail(msID, clients.Session{Key: "ghost", Title: "Nothing"})
	if got := s.ActiveSessions(msID); len(got) != 0 {
		t.Fatalf("detail for a session not playing was kept: %+v", got)
	}

	s.HandleSessionEvent(msID, clients.Session{Key: "b"}, true)
	s.HandleSessionEvent(msID, clients.Session{Key: "a"}, true)
	at := time.Now()
	s.UpdateSessionDetail(msID, clients.Session{Key: "a", Title: "Andor", Position: time.Minute, PositionAt: at})
	s.UpdateSessionDetail(msID, clients.Session{Key: "a", Position: 2 * time.Minute})

	got := s.ActiveSessions(msID)
	if len(got) != 2 || got[0].Key != "a" || got[1].Key != "b" {
		t.Fatalf("sessions %+v, want a then b", got)
	}
	if got[0].Title != "Andor" || !got[0].PositionAt.Equal(at) || got[0].Position != 2*time.Minute {
		t.Errorf("merged detail %+v", got[0])
	}
}

// A snapshot of the same size with different sessions is a change.
func TestReplaceSessionsSwapsSameSizedSets(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	msID, _ := addServerAndDownloader(t, database)
	s.ReplaceSessions(msID, sessionsFrom("a"))
	s.ReplaceSessions(msID, sessionsFrom("b"))
	if got := s.ActiveSessions(msID); len(got) != 1 || got[0].Key != "b" {
		t.Errorf("sessions %+v, want only b", got)
	}
}

// A disabled mapping or downloader is left alone, and a downloader
// switched off is forgotten so switching it back on re-applies its rates.
func TestDisabledMappingsAndDownloaders(t *testing.T) {
	s, database, limiter := newTestScheduler(t)
	msID, dlID := addServerAndDownloader(t, database)
	if err := database.SetMappingForPair(msID, dlID, false); err != nil {
		t.Fatal(err)
	}
	s.HandleSessionEvent(msID, clients.Session{Key: "a"}, true)
	s.resolve(t.Context())
	if s.State(dlID).Throttled {
		t.Error("throttled through a disabled mapping")
	}

	dl, _ := database.GetDownloader(dlID)
	dl.Enabled = false
	if err := database.UpdateDownloader(dl); err != nil {
		t.Fatal(err)
	}
	before := len(limiter.snapshot())
	s.resolve(t.Context())
	if s.State(dlID).Known {
		t.Error("a disabled downloader is still tracked")
	}
	if len(limiter.snapshot()) != before {
		t.Error("rates were sent to a disabled downloader")
	}
}

// Failures that never reach the client: a credential that does not
// decrypt, and a type with no rate-limit support.
func TestApplyFailsBeforeReachingTheClient(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	garbled, _ := database.CreateDownloader(db.Downloader{Name: "garbled", Type: "qbittorrent", URL: "http://a.invalid",
		CredentialEncrypted: sql.NullString{String: "garbled", Valid: true}, Enabled: true})
	s.resolve(t.Context())
	if f := s.State(garbled).Failure; !strings.Contains(f, "reading credential") {
		t.Errorf("failure %q", f)
	}

	s.limiterFor = func(string) clients.RateLimiter { return nil }
	unsupported, _ := database.CreateDownloader(db.Downloader{Name: "other", Type: "transmission", URL: "http://b.invalid", Enabled: true})
	s.resolve(t.Context())
	if f := s.State(unsupported).Failure; !strings.Contains(f, "no rate-limit support") {
		t.Errorf("failure %q", f)
	}
}

// History and settings that cannot be written are logged, and never stop
// throttling.
func TestThrottlingSurvivesAFailingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	limiter := &fakeLimiter{}
	s := New(database, make([]byte, 32), settings.New(database))
	s.limiterFor = func(string) clients.RateLimiter { return limiter }
	s.SetRestoreDelay(0)
	msID, dlID := addServerAndDownloader(t, database)
	if _, err := database.CreateMapping(msID, dlID); err != nil {
		t.Fatal(err)
	}

	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, stmt := range []string{
		`ALTER TABLE throttle_periods RENAME TO gone_periods`,
		`ALTER TABLE events RENAME TO gone_events`,
		`ALTER TABLE session_history RENAME TO gone_history`,
		`ALTER TABLE settings RENAME TO gone_settings`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	s.HandleSessionEvent(msID, clients.Session{Key: "a"}, true)
	s.resolve(t.Context())
	if !s.State(dlID).Throttled {
		t.Fatal("not throttled")
	}
	s.SetPaused(t.Context(), true)
	if !s.Paused() || s.State(dlID).Throttled {
		t.Error("pausing did not take effect")
	}

	s.limiterFor = func(string) clients.RateLimiter { return nil }
	s.SetPaused(context.Background(), false)
	if s.State(dlID).Failure == "" {
		t.Error("a failed apply was not recorded")
	}

	_ = database.Close()
	s.ClearSessions(msID)
}
