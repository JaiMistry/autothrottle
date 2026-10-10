package scheduler

import (
	"context"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/settings"
)

// fakeLimiter records what would have been sent to a download client.
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

// A mapping added mid-playback takes effect on the next resolve.
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

// A rate edit while throttled is pushed, though the state did not change.
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

// A disabled server throttles nothing, even if its sessions survive.
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

// Playback is persisted as it happens.
func TestSessionEventsAreRecordedToHistory(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	msID, _ := addServerAndDownloader(t, database)

	s.HandleSessionEvent(msID, clients.Session{Key: "42"}, true)
	if sessions, err := database.OpenSessions(msID); err != nil || len(sessions) != 1 {
		t.Fatalf("expected one open session, got %+v (%v)", sessions, err)
	}

	s.HandleSessionEvent(msID, clients.Session{Key: "42"}, false)
	if sessions, err := database.OpenSessions(msID); err != nil || len(sessions) != 0 {
		t.Fatalf("expected the session to be closed, got %+v (%v)", sessions, err)
	}
}

func openKeys(t *testing.T, database *db.DB, msID int64) []string {
	t.Helper()
	sessions, err := database.OpenSessions(msID)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, session := range sessions {
		keys = append(keys, session.SessionKey)
	}
	sort.Strings(keys)
	return keys
}

// A session only a snapshot saw still lands in history.
func TestReplaceSessionsRecordsHistory(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	msID, _ := addServerAndDownloader(t, database)

	s.ReplaceSessions(msID, sessionsFrom("a", "b"))
	if got := openKeys(t, database, msID); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("open sessions = %v, want both from the snapshot", got)
	}
	s.ReplaceSessions(msID, sessionsFrom("b"))
	if got := openKeys(t, database, msID); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("open sessions = %v, want only b", got)
	}
}

// A stopped watcher never sees its sessions end, so they are closed for it.
// A server whose watcher stops must not keep its downloaders throttled.
func TestClearSessionsReleasesAndClosesHistory(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	msID, dlID := addServerAndDownloader(t, database)
	if _, err := database.CreateMapping(msID, dlID); err != nil {
		t.Fatal(err)
	}

	s.HandleSessionEvent(msID, clients.Session{Key: "42"}, true)
	s.resolve(context.Background())
	if !s.State(dlID).Throttled {
		t.Fatal("not throttled while a session is active")
	}
	s.ClearSessions(msID)
	eventually(t, "the release", func() bool { st := s.State(dlID); return st.Known && !st.Throttled })
	if got := openKeys(t, database, msID); len(got) != 0 {
		t.Fatalf("open sessions = %v, want none", got)
	}
}

// The log attributes a throttle to its server by id, not by name.
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

func sessionsFrom(keys ...string) []clients.Session {
	out := make([]clients.Session, 0, len(keys))
	for _, key := range keys {
		out = append(out, clients.Session{Key: key})
	}
	return out
}

// A period spans the whole time anything is throttled, and pausing
// closes it.
func TestThrottlePeriodFollowsThrottling(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	msID, dlID := addServerAndDownloader(t, database)
	if _, err := database.CreateMapping(msID, dlID); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	periods := func() []db.Span {
		t.Helper()
		p, err := database.ThrottlePeriods(time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	if _, ok := s.ThrottlingSince(); ok {
		t.Fatal("throttling before anything played")
	}
	s.HandleSessionEvent(msID, clients.Session{Key: "a"}, true)
	s.resolve(ctx)
	since, ok := s.ThrottlingSince()
	if !ok || len(periods()) != 1 {
		t.Fatalf("playback did not open a period: since %v, periods %+v", since, periods())
	}

	s.HandleSessionEvent(msID, clients.Session{Key: "b"}, true)
	s.resolve(ctx)
	if again, _ := s.ThrottlingSince(); !again.Equal(since) || len(periods()) != 1 {
		t.Errorf("a second session restarted the period")
	}

	s.SetPaused(ctx, true)
	if _, ok := s.ThrottlingSince(); ok || periods()[0].End.IsZero() {
		t.Errorf("pausing did not close the period: %+v", periods())
	}
	s.SetPaused(ctx, false)
	if _, ok := s.ThrottlingSince(); !ok || len(periods()) != 2 {
		t.Errorf("resuming did not open a new period: %+v", periods())
	}
}
