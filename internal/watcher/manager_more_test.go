package watcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/db"
)

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Detail goes to the scheduler as detail, everything else as an event;
// a closed channel ends the pump.
func TestForwardRoutesEvents(t *testing.T) {
	m, _, sink, _ := newTestManager(t)
	events := make(chan clients.SessionEvent, 3)
	events <- clients.SessionEvent{Session: clients.Session{Key: "a"}, Active: true}
	events <- clients.SessionEvent{Session: clients.Session{Key: "a", Title: "Andor"}, Active: true, DetailOnly: true}
	events <- clients.SessionEvent{Session: clients.Session{Key: "a"}}
	close(events)
	m.forward(context.Background(), 1, events)
	if got := sink.eventList(); len(got) != 2 || got[0] != "a:true" || got[1] != "a:false" {
		t.Errorf("events %v", got)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.detail) != 1 || sink.detail[0].Title != "Andor" {
		t.Errorf("detail %+v", sink.detail)
	}
}

// Losing contact is logged once, however long it lasts, and regaining it
// is logged too.
func TestReconcileLogsLostAndRegainedContact(t *testing.T) {
	m, database, sink, key := newTestManager(t)
	m.reconcileEvery = 5 * time.Millisecond
	id := addServer(t, database, key, "Plex", "http://plex.invalid:32400", false)
	w := &fakeWatcher{err: errors.New("connection refused"), snapshot: sessionsFrom("s")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.reconcileSessions(ctx, id, "Plex", w)

	time.Sleep(60 * time.Millisecond) // several failed passes
	w.setErr(nil)
	eventually(t, "the recovery", func() bool { return len(sink.replacements()) > 0 })

	events, err := database.ListEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	var lost, back int
	for _, e := range events {
		switch e.Message {
		case "Lost contact with Plex. Playback there will not be noticed until it answers again.":
			lost++
		case "Back in contact with Plex":
			back++
		}
	}
	if lost != 1 || back != 1 {
		t.Errorf("lost %d, back %d, want one of each in %+v", lost, back, events)
	}
}

// Startup snapshots every watcher before throttling is resolved, and a
// failing one does not hold up the rest.
func TestSnapshotAllAndConnection(t *testing.T) {
	m, _, sink, _ := newTestManager(t)
	m.mu.Lock()
	m.running[1] = handle{cancel: func() {}, watcher: &fakeWatcher{snapshot: sessionsFrom("s")}}
	m.running[2] = handle{cancel: func() {}, watcher: &fakeWatcher{err: errors.New("refused")}}
	m.mu.Unlock()

	m.SnapshotAll(context.Background())
	if got := sink.replacements(); len(got) != 1 || got[0].mediaServerID != 1 {
		t.Errorf("replacements %+v", got)
	}
	if state, ok := m.Connection(1); !ok || !state.Connected {
		t.Error("a running watcher reported no connection")
	}
	if _, ok := m.Connection(99); ok {
		t.Error("a server with no watcher reported a connection")
	}
}

// What cannot be run is skipped, and a database that cannot be read
// changes nothing.
func TestReconcileSkipsWhatItCannotRun(t *testing.T) {
	m, database, _, key := newTestManager(t)
	bad, err := database.CreateMediaServer(db.MediaServer{Name: "Bad", Type: "plex", URL: "http://bad.invalid", CredentialEncrypted: "not encrypted", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	local := addServer(t, database, key, "Local", "http://local.invalid:32400", true)
	ms, _ := database.GetMediaServer(local)
	ms.SkipLocalPlayback = true
	_ = database.UpdateMediaServer(ms)

	m.Reconcile()
	if m.Running(bad) || !m.Running(local) {
		t.Errorf("running: bad %v, local %v", m.Running(bad), m.Running(local))
	}

	// Before Start there is nothing to reconcile into.
	early := NewManager(database, key, &fakeSink{})
	early.Reconcile()
	if len(early.running) != 0 {
		t.Error("reconciled before Start")
	}

	_ = database.Close()
	m.Reconcile()
	m.note("info", "not recorded", 0)
	if !m.Running(local) {
		t.Error("an unreadable database stopped a running watcher")
	}
}

func TestPauseStoreRoundTrip(t *testing.T) {
	_, database, _, key := newTestManager(t)
	id := addServer(t, database, key, "Plex", "http://plex.invalid:32400", false)
	store := pauseStore{db: database, mediaServerID: id}
	since := time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC)

	if err := store.Save("a", clients.PausedSession{Since: since, Position: time.Minute}); err != nil {
		t.Fatal(err)
	}
	_ = store.Save("b", clients.PausedSession{Since: since})
	if err := store.Retain(map[string]bool{"a": true}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil || len(got) != 1 || got["a"].Position != time.Minute || !got["a"].Since.Equal(since) {
		t.Fatalf("loaded %+v, %v", got, err)
	}
	if err := store.Delete("a"); err != nil {
		t.Fatal(err)
	}
	_ = database.Close()
	if _, err := store.Load(); err == nil {
		t.Error("loaded from a closed database")
	}
}
