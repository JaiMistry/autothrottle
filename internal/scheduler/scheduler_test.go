package scheduler

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

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

	s.HandleSessionEvent(msID, "session-1", true)
	s.Resolve(context.Background())
	if got := limiter.snapshot(); len(got) != 1 || got[0].download != 0 {
		t.Fatalf("expected one default-rate call before any mapping exists, got %+v", got)
	}

	if _, err := database.CreateMapping(msID, dlID); err != nil {
		t.Fatalf("creating mapping: %v", err)
	}
	s.Resolve(context.Background())

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

	s.HandleSessionEvent(msID, "session-1", true)
	s.Resolve(context.Background())

	dl, err := database.GetDownloader(dlID)
	if err != nil {
		t.Fatalf("getting downloader: %v", err)
	}
	dl.BackupDownloadLimit = 1000
	if err := database.UpdateDownloader(dl); err != nil {
		t.Fatalf("updating downloader: %v", err)
	}
	s.Resolve(context.Background())

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

	s.HandleSessionEvent(msID, "session-1", true)
	s.Resolve(context.Background())
	s.Resolve(context.Background())
	s.Resolve(context.Background())

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

	s.HandleSessionEvent(msID, "session-1", true)
	s.Resolve(context.Background())

	ms, err := database.GetMediaServer(msID)
	if err != nil {
		t.Fatalf("getting media server: %v", err)
	}
	ms.Enabled = false
	if err := database.UpdateMediaServer(ms); err != nil {
		t.Fatalf("disabling media server: %v", err)
	}
	s.Resolve(context.Background())

	got := limiter.snapshot()
	if len(got) != 2 {
		t.Fatalf("expected disabling the server to restore default rates, got %+v", got)
	}
	if got[1].download != 0 || got[1].upld != 0 {
		t.Errorf("expected default (unlimited) rates after disable, got %d/%d", got[1].download, got[1].upld)
	}
	if throttled, _ := s.DownloaderThrottled(dlID); throttled {
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

	s.HandleSessionEvent(msID, "session-1", true)
	s.Resolve(context.Background())
	if throttled, known := s.DownloaderThrottled(dlID); !known || !throttled {
		t.Fatal("expected downloader to be throttled while a session is active")
	}

	s.mu.Lock()
	delete(s.active, msID)
	s.mu.Unlock()
	s.Resolve(context.Background())

	if throttled, _ := s.DownloaderThrottled(dlID); throttled {
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

	s.HandleSessionEvent(msID, "session-1", true)
	s.Resolve(context.Background())

	s.SetPaused(context.Background(), true)
	if throttled, _ := s.DownloaderThrottled(dlID); throttled {
		t.Error("pause must release the throttle even while a session is active")
	}

	s.SetPaused(context.Background(), false)
	if throttled, known := s.DownloaderThrottled(dlID); !known || !throttled {
		t.Error("unpausing must re-throttle against still-active sessions")
	}
	if got := limiter.snapshot(); len(got) != 3 {
		t.Errorf("expected throttle, release, re-throttle, got %+v", got)
	}
}
