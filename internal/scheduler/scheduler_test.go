package scheduler

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/settings"
)

// fakeLimiter records what would have been sent to a download client.
// Every test here runs against this — never a real qBittorrent.
type fakeLimiter struct {
	mu    sync.Mutex
	calls []call
}

type call struct {
	url            string
	download, upld int64
}

func (f *fakeLimiter) SetLimits(_ context.Context, baseURL, _, _ string, down, up int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{url: baseURL, download: down, upld: up})
	return nil
}

func (f *fakeLimiter) snapshot() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

func newTestScheduler(t *testing.T) (*Scheduler, *db.DB, *fakeLimiter) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	limiter := &fakeLimiter{}
	s := New(database, make([]byte, 32), settings.New(database))
	s.limiterFor = func(string) clients.RateLimiter { return limiter }
	return s, database, limiter
}

func addServerAndDownloader(t *testing.T, database *db.DB) (int64, int64) {
	t.Helper()
	msID, err := database.CreateMediaServer(db.MediaServer{
		Name: "Plex", Type: "plex", URL: "http://plex.invalid:32400",
		CredentialEncrypted: "unused-in-these-tests", Enabled: true,
	})
	if err != nil {
		t.Fatalf("creating media server: %v", err)
	}
	dlID, err := database.CreateDownloader(db.Downloader{
		Name: "qbit", Type: "qbittorrent", URL: "http://qbit.invalid",
		DefaultDownloadLimit: 0, DefaultUploadLimit: 0,
		BackupDownloadLimit: 500, BackupUploadLimit: 100,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("creating downloader: %v", err)
	}
	return msID, dlID
}

// A mapping created after the fact must take effect on the next
// resolve, without waiting for a playback event — the whole point of
// resolving from the database on every pass.
func TestMappingAddedWhileSessionActiveThrottles(t *testing.T) {
	s, database, limiter := newTestScheduler(t)
	msID, dlID := addServerAndDownloader(t, database)

	s.HandleSessionEvent(msID, clients.Session{Key: "session-1"}, true)
	s.resolve(context.Background())
	if got := limiter.snapshot(); len(got) != 1 || got[0].download != 0 {
		t.Fatalf("expected one default-rate call before any mapping exists, got %+v", got)
	}

	if _, err := database.CreateMapping(msID, dlID); err != nil {
		t.Fatalf("creating mapping: %v", err)
	}
	s.resolve(context.Background())

	got := limiter.snapshot()
	if len(got) != 2 {
		t.Fatalf("expected the new mapping to throttle immediately, got %d calls: %+v", len(got), got)
	}
	if got[1].download != 500 || got[1].upld != 100 {
		t.Errorf("expected backup rates 500/100, got %d/%d", got[1].download, got[1].upld)
	}
}

// Editing a rate while a downloader is already throttled has to push
// the new numbers — the state didn't change, but what's applied did.
func TestRateEditWhileThrottledIsPushed(t *testing.T) {
	s, database, limiter := newTestScheduler(t)
	msID, dlID := addServerAndDownloader(t, database)
	if _, err := database.CreateMapping(msID, dlID); err != nil {
		t.Fatalf("creating mapping: %v", err)
	}

	s.HandleSessionEvent(msID, clients.Session{Key: "session-1"}, true)
	s.resolve(context.Background())

	dl, err := database.GetDownloader(dlID)
	if err != nil {
		t.Fatalf("getting downloader: %v", err)
	}
	dl.BackupDownloadLimit = 1000
	if err := database.UpdateDownloader(dl); err != nil {
		t.Fatalf("updating downloader: %v", err)
	}
	s.resolve(context.Background())

	got := limiter.snapshot()
	if len(got) != 2 {
		t.Fatalf("expected the edited rate to be pushed, got %d calls: %+v", len(got), got)
	}
	if got[1].download != 1000 {
		t.Errorf("expected new backup rate 1000, got %d", got[1].download)
	}
}

// An unchanged resolve must not re-hit the download client.
func TestUnchangedResolveIsNotReapplied(t *testing.T) {
	s, database, limiter := newTestScheduler(t)
	msID, dlID := addServerAndDownloader(t, database)
	if _, err := database.CreateMapping(msID, dlID); err != nil {
		t.Fatalf("creating mapping: %v", err)
	}

	s.HandleSessionEvent(msID, clients.Session{Key: "session-1"}, true)
	s.resolve(context.Background())
	s.resolve(context.Background())
	s.resolve(context.Background())

	if got := limiter.snapshot(); len(got) != 1 {
		t.Fatalf("expected exactly one call for an unchanged state, got %d: %+v", len(got), got)
	}
}

// A disabled media server must not throttle anything, even if session
// state for it survives somewhere.
func TestDisabledMediaServerDoesNotThrottle(t *testing.T) {
	s, database, limiter := newTestScheduler(t)
	msID, dlID := addServerAndDownloader(t, database)
	if _, err := database.CreateMapping(msID, dlID); err != nil {
		t.Fatalf("creating mapping: %v", err)
	}

	s.HandleSessionEvent(msID, clients.Session{Key: "session-1"}, true)
	s.resolve(context.Background())

	ms, err := database.GetMediaServer(msID)
	if err != nil {
		t.Fatalf("getting media server: %v", err)
	}
	ms.Enabled = false
	if err := database.UpdateMediaServer(ms); err != nil {
		t.Fatalf("disabling media server: %v", err)
	}
	s.resolve(context.Background())

	got := limiter.snapshot()
	if len(got) != 2 {
		t.Fatalf("expected disabling the server to restore default rates, got %+v", got)
	}
	if got[1].download != 0 || got[1].upld != 0 {
		t.Errorf("expected default (unlimited) rates after disable, got %d/%d", got[1].download, got[1].upld)
	}
	if s.State(dlID).Throttled {
		t.Error("downloader should not report throttled once its only server is disabled")
	}
}

// Clearing a stopped watcher's sessions must release the throttle.
func TestClearSessionsReleasesThrottle(t *testing.T) {
	s, database, limiter := newTestScheduler(t)
	msID, dlID := addServerAndDownloader(t, database)
	if _, err := database.CreateMapping(msID, dlID); err != nil {
		t.Fatalf("creating mapping: %v", err)
	}

	s.HandleSessionEvent(msID, clients.Session{Key: "session-1"}, true)
	s.resolve(context.Background())
	if st := s.State(dlID); !st.Known || !st.Throttled {
		t.Fatal("expected downloader to be throttled while a session is active")
	}

	s.mu.Lock()
	delete(s.active, msID)
	s.mu.Unlock()
	s.resolve(context.Background())

	if s.State(dlID).Throttled {
		t.Error("expected throttle to release once sessions are cleared")
	}
	if got := limiter.snapshot(); len(got) != 2 || got[1].download != 0 {
		t.Errorf("expected default rates restored, got %+v", got)
	}
}

// The manual pause override outranks active sessions.
func TestPauseOverridesActiveSession(t *testing.T) {
	s, database, limiter := newTestScheduler(t)
	msID, dlID := addServerAndDownloader(t, database)
	if _, err := database.CreateMapping(msID, dlID); err != nil {
		t.Fatalf("creating mapping: %v", err)
	}

	s.HandleSessionEvent(msID, clients.Session{Key: "session-1"}, true)
	s.resolve(context.Background())

	s.SetPaused(context.Background(), true)
	if s.State(dlID).Throttled {
		t.Error("pause must release the throttle even while a session is active")
	}

	s.SetPaused(context.Background(), false)
	if st := s.State(dlID); !st.Known || !st.Throttled {
		t.Error("unpausing must re-throttle against still-active sessions")
	}
	if got := limiter.snapshot(); len(got) != 3 {
		t.Errorf("expected throttle, release, re-throttle, got %+v", got)
	}
}

// Session history feeds the Analytics timeline, so playback has to be
// persisted as it happens — it cannot be reconstructed later from the
// event log, which only records throttle transitions.
func TestSessionEventsAreRecordedToHistory(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	msID, _ := addServerAndDownloader(t, database)
	window := time.Now().Add(-time.Hour)

	s.HandleSessionEvent(msID, clients.Session{Key: "42"}, true)
	sessions, err := database.ListSessions(0, window, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Ended() {
		t.Fatalf("expected one open session, got %+v", sessions)
	}

	s.HandleSessionEvent(msID, clients.Session{Key: "42"}, false)
	sessions, err = database.ListSessions(0, window, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 || !sessions[0].Ended() {
		t.Fatalf("expected the session to be closed, got %+v", sessions)
	}
}

// Reconciliation snapshots are authoritative, so a session the event
// stream missed must still land in history.
func TestReplaceSessionsRecordsHistory(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	msID, _ := addServerAndDownloader(t, database)
	window := time.Now().Add(-time.Hour)

	s.ReplaceSessions(msID, sessionsFrom("a", "b"))
	sessions, err := database.ListSessions(0, window, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected two sessions from the snapshot, got %+v", sessions)
	}

	// "a" ended, "b" continues.
	s.ReplaceSessions(msID, sessionsFrom("b"))
	sessions, err = database.ListSessions(0, window, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range sessions {
		switch session.SessionKey {
		case "a":
			if !session.Ended() {
				t.Error("a session missing from the snapshot must be closed")
			}
		case "b":
			if session.Ended() {
				t.Error("a session still in the snapshot must stay open")
			}
		}
	}
}

// A stopped watcher can never observe its sessions ending, so they must
// not be left running to the right edge of every future chart.
func TestClearSessionsClosesHistory(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	msID, _ := addServerAndDownloader(t, database)

	s.HandleSessionEvent(msID, clients.Session{Key: "42"}, true)
	s.ClearSessions(msID)

	sessions, err := database.ListSessions(0, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || !sessions[0].Ended() {
		t.Fatalf("expected the session to be closed, got %+v", sessions)
	}
}

// Analytics needs to attribute a throttle to the server that caused it.
// The server's name is only inside the message text; the id is what a
// query can actually use.
func TestThrottleEventNamesTheCausingServer(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	msID, dlID := addServerAndDownloader(t, database)
	if _, err := database.CreateMapping(msID, dlID); err != nil {
		t.Fatal(err)
	}

	s.HandleSessionEvent(msID, clients.Session{Key: "42"}, true)
	s.resolve(context.Background())

	events, err := database.ListEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range events {
		if e.MediaServerID.Valid && e.MediaServerID.Int64 == msID {
			found = true
		}
	}
	if !found {
		t.Errorf("no event attributed to media server %d: %+v", msID, events)
	}
}

// sessionsFrom builds the snapshot shape from bare keys, for the many
// tests that only care about which sessions exist.
func sessionsFrom(keys ...string) []clients.Session {
	out := make([]clients.Session, 0, len(keys))
	for _, key := range keys {
		out = append(out, clients.Session{Key: key})
	}
	return out
}
