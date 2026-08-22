package watcher

import (
	"context"
	"errors"
	"testing"

	"github.com/JaiMistry/autothrottle/internal/clients"
)

// fakeWatcher is a SessionWatcher that never connects to anything.
type fakeWatcher struct {
	events   chan clients.SessionEvent
	snapshot []clients.Session
	err      error
	calls    int
}

func (f *fakeWatcher) Run(context.Context) {}

func (f *fakeWatcher) Events() <-chan clients.SessionEvent {
	if f.events == nil {
		f.events = make(chan clients.SessionEvent)
	}
	return f.events
}

func (f *fakeWatcher) Snapshot(context.Context) ([]clients.Session, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.snapshot, nil
}

func TestReconcileOnceForwardsSnapshotToScheduler(t *testing.T) {
	m, _, sink, _ := newTestManager(t)
	w := &fakeWatcher{snapshot: sessionsFrom("session-1", "session-2")}

	if err := m.reconcileOnce(context.Background(), 7, w); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}

	got := sink.replacements()
	if len(got) != 1 {
		t.Fatalf("got %d replacements, want 1", len(got))
	}
	if got[0].mediaServerID != 7 || len(got[0].sessionKeys) != 2 {
		t.Errorf("got %+v, want the snapshot for server 7", got[0])
	}
}

// An unreachable server must leave the tracked session set alone.
// Treating a failed snapshot as "nothing is playing" would drop
// throttling every time the server hiccuped mid-playback — worse than
// the drift the poll exists to correct.
func TestReconcileOnceLeavesStateAloneWhenSnapshotFails(t *testing.T) {
	m, _, sink, _ := newTestManager(t)
	w := &fakeWatcher{err: errors.New("connection refused")}

	if err := m.reconcileOnce(context.Background(), 7, w); err == nil {
		t.Fatal("expected an error from a failed snapshot")
	}

	if got := sink.replacements(); len(got) != 0 {
		t.Errorf("got %d replacements, want none on failure", len(got))
	}
}

// An empty snapshot is a real answer — "nothing is playing" — and must
// be forwarded, since that's exactly the drift case where a session
// stopped without the event stream ever saying so.
func TestReconcileOnceForwardsEmptySnapshot(t *testing.T) {
	m, _, sink, _ := newTestManager(t)
	w := &fakeWatcher{snapshot: nil}

	if err := m.reconcileOnce(context.Background(), 7, w); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}

	got := sink.replacements()
	if len(got) != 1 || len(got[0].sessionKeys) != 0 {
		t.Errorf("got %+v, want one empty replacement", got)
	}
}

// Connection satisfies the interface; these tests do not exercise the
// live-feed health surface.
func (f *fakeWatcher) Connection() clients.ConnectionState {
	return clients.ConnectionState{Connected: true}
}

// sessionsFrom builds the snapshot shape from bare keys.
func sessionsFrom(keys ...string) []clients.Session {
	out := make([]clients.Session, 0, len(keys))
	for _, key := range keys {
		out = append(out, clients.Session{Key: key})
	}
	return out
}
