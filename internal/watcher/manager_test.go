package watcher

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/crypto"
	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/live"
)

// fakeSink stands in for the scheduler. Watchers here dial .invalid hosts.
type fakeSink struct {
	detail   []clients.Session
	mu       sync.Mutex
	cleared  []int64
	replaced []replacement
	events   []string
}

type replacement struct {
	mediaServerID int64
	sessionKeys   []string
}

func (f *fakeSink) HandleSessionEvent(_ int64, session clients.Session, active bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, fmt.Sprintf("%s:%v", session.Key, active))
}

func (f *fakeSink) eventList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func (f *fakeSink) ReplaceSessions(mediaServerID int64, sessions []clients.Session) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replaced = append(f.replaced, replacement{mediaServerID: mediaServerID, sessionKeys: keysOf(sessions)})
}

func (f *fakeSink) UpdateSessionDetail(mediaServerID int64, session clients.Session) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detail = append(f.detail, session)
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
	m := NewManager(database, key, sink)

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

// Disabling or deleting a server stops its watcher and releases its
// sessions, so it cannot keep downloaders throttled.
func TestReconcileStopsRemovedServer(t *testing.T) {
	for name, remove := range map[string]func(*db.DB, int64) error{
		"disabled": func(d *db.DB, id int64) error {
			ms, err := d.GetMediaServer(id)
			if err != nil {
				return err
			}
			ms.Enabled = false
			return d.UpdateMediaServer(ms)
		},
		"deleted": func(d *db.DB, id int64) error { return d.DeleteMediaServer(id) },
	} {
		t.Run(name, func(t *testing.T) {
			m, database, sink, key := newTestManager(t)
			id := addServer(t, database, key, "Plex", "http://plex-b.invalid:32400", true)
			m.Reconcile()
			if err := remove(database, id); err != nil {
				t.Fatal(err)
			}
			m.Reconcile()
			if m.Running(id) {
				t.Error("the watcher is still running")
			}
			if got := sink.clearedIDs(); len(got) != 1 || got[0] != id {
				t.Errorf("sessions cleared for %v, want %d", got, id)
			}
		})
	}
}

// A watcher captures its URL at construction, so a change replaces it.
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

// Nothing changed means no redial.
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

func TestSupportedTypes(t *testing.T) {
	for _, msType := range []string{"plex", "emby", "jellyfin"} {
		if !clients.SupportedMediaServerType(msType) {
			t.Errorf("SupportedMediaServerType(%q) = false, want true", msType)
		}
		w, err := (&Manager{}).newWatcher(db.MediaServer{Type: msType, URL: "http://host"}, "token")
		if err != nil {
			t.Errorf("newWatcher(%q): %v", msType, err)
			continue
		}
		if w == nil {
			t.Errorf("newWatcher(%q) returned a nil watcher", msType)
		}
	}

	if clients.SupportedMediaServerType("transmission") {
		t.Error(`SupportedMediaServerType("transmission") = true, want false`)
	}
	if _, err := (&Manager{}).newWatcher(db.MediaServer{Type: "nope"}, "t"); err == nil {
		t.Error("newWatcher for an unknown type should return an error")
	}
}

func keysOf(sessions []clients.Session) []string {
	out := make([]string, 0, len(sessions))
	for _, session := range sessions {
		out = append(out, session.Key)
	}
	return out
}

type heard struct {
	mu sync.Mutex
	n  int
}

func (h *heard) Notify(live.Topic) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.n++
}

func (h *heard) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.n
}

// A watcher starting is announced without waiting for a session event.
func TestHealthChangesAreAnnounced(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	key := make([]byte, 32)
	m := NewManager(database, key, &fakeSink{})
	h := &heard{}
	m.SetNotifier(h) // before Start, which is when the health watch begins
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m.Start(ctx)

	time.Sleep(2*healthCheckInterval + 500*time.Millisecond)
	if h.count() != 0 {
		t.Fatalf("announced %d changes with nothing running", h.count())
	}
	addServer(t, database, key, "Plex", "http://plex-h.invalid:32400", true)
	m.Reconcile()
	deadline := time.Now().Add(3 * healthCheckInterval)
	for h.count() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("a watcher starting was never announced")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
