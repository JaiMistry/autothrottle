package watcher

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/JaiMistry/autothrottle/internal/crypto"
	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/settings"
)

// fakeSink stands in for the scheduler. Watchers started here dial a
// .invalid host, so they never reach any real media server.
type fakeSink struct {
	mu       sync.Mutex
	cleared  []int64
	replaced []replacement
}

type replacement struct {
	mediaServerID int64
	sessionKeys   []string
}

func (f *fakeSink) HandleSessionEvent(int64, string, bool) {}

func (f *fakeSink) ReplaceSessions(mediaServerID int64, sessionKeys []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replaced = append(f.replaced, replacement{mediaServerID: mediaServerID, sessionKeys: sessionKeys})
}

func (f *fakeSink) replacements() []replacement {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]replacement(nil), f.replaced...)
}

func (f *fakeSink) ClearSessions(mediaServerID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = append(f.cleared, mediaServerID)
}

func (f *fakeSink) clearedIDs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.cleared...)
}

func newTestManager(t *testing.T) (*Manager, *db.DB, *fakeSink, []byte) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	key := make([]byte, 32)
	sink := &fakeSink{}
	m := NewManager(database, key, sink, settings.New(database))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m.Start(ctx)

	return m, database, sink, key
}

func addServer(t *testing.T, database *db.DB, key []byte, name, url string, enabled bool) int64 {
	t.Helper()
	token, err := crypto.Encrypt(key, "test-token")
	if err != nil {
		t.Fatalf("encrypting token: %v", err)
	}
	id, err := database.CreateMediaServer(db.MediaServer{
		Name: name, Type: "plex", URL: url,
		CredentialEncrypted: token, Enabled: enabled,
	})
	if err != nil {
		t.Fatalf("creating media server: %v", err)
	}
	return id
}

// Adding a server must start watching it without a restart.
func TestReconcileStartsNewServer(t *testing.T) {
	m, database, _, key := newTestManager(t)

	id := addServer(t, database, key, "Plex", "http://plex-a.invalid:32400", true)
	if m.Running(id) {
		t.Fatal("watcher should not exist before reconcile")
	}

	m.Reconcile()
	if !m.Running(id) {
		t.Error("expected a watcher to start for the newly added server")
	}
}

// Disabling a server must stop its watcher and release its sessions,
// so it can't keep downloaders throttled from beyond the grave.
func TestReconcileStopsDisabledServer(t *testing.T) {
	m, database, sink, key := newTestManager(t)
	id := addServer(t, database, key, "Plex", "http://plex-b.invalid:32400", true)
	m.Reconcile()

	ms, err := database.GetMediaServer(id)
	if err != nil {
		t.Fatalf("getting media server: %v", err)
	}
	ms.Enabled = false
	if err := database.UpdateMediaServer(ms); err != nil {
		t.Fatalf("disabling media server: %v", err)
	}
	m.Reconcile()

	if m.Running(id) {
		t.Error("expected the watcher to stop when its server is disabled")
	}
	if got := sink.clearedIDs(); len(got) != 1 || got[0] != id {
		t.Errorf("expected sessions cleared for %d, got %v", id, got)
	}
}

// Deleting a server must stop its watcher.
func TestReconcileStopsDeletedServer(t *testing.T) {
	m, database, sink, key := newTestManager(t)
	id := addServer(t, database, key, "Plex", "http://plex-c.invalid:32400", true)
	m.Reconcile()

	if err := database.DeleteMediaServer(id); err != nil {
		t.Fatalf("deleting media server: %v", err)
	}
	m.Reconcile()

	if m.Running(id) {
		t.Error("expected the watcher to stop when its server is deleted")
	}
	if got := sink.clearedIDs(); len(got) != 1 || got[0] != id {
		t.Errorf("expected sessions cleared for %d, got %v", id, got)
	}
}

// A URL change must replace the running watcher — it captured the old
// URL at construction and can't be updated in place.
func TestReconcileRestartsOnURLChange(t *testing.T) {
	m, database, sink, key := newTestManager(t)
	id := addServer(t, database, key, "Plex", "http://plex-d.invalid:32400", true)
	m.Reconcile()

	ms, err := database.GetMediaServer(id)
	if err != nil {
		t.Fatalf("getting media server: %v", err)
	}
	ms.URL = "http://plex-moved.invalid:32400"
	if err := database.UpdateMediaServer(ms); err != nil {
		t.Fatalf("updating media server: %v", err)
	}
	m.Reconcile()

	if !m.Running(id) {
		t.Error("expected a replacement watcher to be running after a URL change")
	}
	if got := sink.clearedIDs(); len(got) != 1 {
		t.Errorf("expected the stale watcher's sessions to be cleared once, got %v", got)
	}
}

// Reconciling with nothing changed must leave the existing connection
// alone rather than redialling.
func TestReconcileIsIdempotent(t *testing.T) {
	m, database, sink, key := newTestManager(t)
	id := addServer(t, database, key, "Plex", "http://plex-e.invalid:32400", true)

	m.Reconcile()
	m.Reconcile()
	m.Reconcile()

	if !m.Running(id) {
		t.Error("expected the watcher to still be running")
	}
	if got := sink.clearedIDs(); len(got) != 0 {
		t.Errorf("expected no watcher churn on an unchanged reconcile, got %v", got)
	}
}
