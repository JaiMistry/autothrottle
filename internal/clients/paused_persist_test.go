package clients

import (
	"testing"
	"time"
)

type memPauseStore struct {
	saved map[string]PausedSession
	loads int
}

func newMemPauseStore() *memPauseStore {
	return &memPauseStore{saved: map[string]PausedSession{}}
}

func (m *memPauseStore) Load() (map[string]PausedSession, error) {
	m.loads++
	out := make(map[string]PausedSession, len(m.saved))
	for k, v := range m.saved {
		out[k] = v
	}
	return out, nil
}
func (m *memPauseStore) Save(key string, state PausedSession) error {
	m.saved[key] = state
	return nil
}
func (m *memPauseStore) Delete(key string) error { delete(m.saved, key); return nil }
func (m *memPauseStore) Retain(present map[string]bool) error {
	for k := range m.saved {
		if !present[k] {
			delete(m.saved, k)
		}
	}
	return nil
}

func tracker(store PauseStore, timeout time.Duration) *pausedTracker {
	p := newPausedTracker(timeout)
	p.store = store
	return p
}

// A stream retired as abandoned must stay retired across a restart.
func TestPausedClockSurvivesRestart(t *testing.T) {
	const limit = 30 * time.Minute
	store := newMemPauseStore()
	start := time.Now()

	first := tracker(store, limit)
	if !first.counts("s1", true, 5*time.Minute, start) {
		t.Fatal("a freshly paused session should still count")
	}
	if first.counts("s1", true, 5*time.Minute, start.Add(limit+time.Minute)) {
		t.Fatal("past the timeout it should have been retired")
	}

	// A restart: new tracker, same store, still paused in the same place.
	second := tracker(store, limit)
	if second.counts("s1", true, 5*time.Minute, start.Add(limit+2*time.Minute)) {
		t.Error("a retired session came back after a restart")
	}
	if store.loads == 0 {
		t.Error("the new tracker never read the persisted clock")
	}
}

// Resuming while the app was down moved the position, so the old clock
// no longer applies.
func TestResumeWhileDownClearsTheClock(t *testing.T) {
	const limit = 30 * time.Minute
	store := newMemPauseStore()
	start := time.Now()

	first := tracker(store, limit)
	first.counts("s1", true, 5*time.Minute, start)

	second := tracker(store, limit)
	if !second.counts("s1", false, 9*time.Minute, start.Add(limit+time.Minute)) {
		t.Error("a session that moved on should count again")
	}
	if _, still := store.saved["s1"]; still {
		t.Error("the pause record should have been cleared once it resumed")
	}
}

func TestRetainPrunesPersistedPauses(t *testing.T) {
	store := newMemPauseStore()
	p := tracker(store, time.Minute)
	p.counts("gone", true, time.Minute, time.Now())
	p.counts("here", true, time.Minute, time.Now())

	p.retain(map[string]bool{"here": true})
	if _, ok := store.saved["gone"]; ok {
		t.Error("a session the server dropped kept its persisted pause")
	}
	if _, ok := store.saved["here"]; !ok {
		t.Error("a session still listed lost its persisted pause")
	}
}
