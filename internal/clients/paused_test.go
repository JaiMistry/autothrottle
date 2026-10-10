package clients

import (
	"testing"
	"time"
)

func TestPausedTrackerCountsUntilTimeout(t *testing.T) {
	tracker := newPausedTracker(time.Minute)
	start := time.Date(2026, 8, 22, 20, 0, 0, 0, time.UTC)

	if !tracker.counts("a", true, 0, start) {
		t.Error("a session that has just paused must still count")
	}
	if !tracker.counts("a", true, 0, start.Add(59*time.Second)) {
		t.Error("still within the timeout, so it must still count")
	}
	if tracker.counts("a", true, 0, start.Add(61*time.Second)) {
		t.Error("past the timeout, an abandoned pause must stop counting")
	}
}

// Resuming resets the clock, so brief pauses never add up to the cap.
func TestPausedTrackerResetsOnResume(t *testing.T) {
	tracker := newPausedTracker(time.Minute)
	start := time.Date(2026, 8, 22, 20, 0, 0, 0, time.UTC)

	tracker.counts("a", true, 0, start)
	if !tracker.counts("a", false, 0, start.Add(30*time.Second)) {
		t.Fatal("a playing session always counts")
	}
	// Paused again: the timeout restarts from here.
	if !tracker.counts("a", true, 0, start.Add(31*time.Second)) {
		t.Error("a fresh pause must count")
	}
	if !tracker.counts("a", true, 0, start.Add(80*time.Second)) {
		t.Error("the timeout should have restarted on resume, not carried over")
	}
}

func TestPausedTrackerForgetsEndedSessions(t *testing.T) {
	tracker := newPausedTracker(time.Minute)
	start := time.Date(2026, 8, 22, 20, 0, 0, 0, time.UTC)

	tracker.counts("a", true, 0, start)
	tracker.forget("a")
	if !tracker.counts("a", true, 0, start.Add(2*time.Minute)) {
		t.Error("a forgotten session must start its pause clock fresh")
	}
}

func TestPausedTrackerRetainDropsAbsentSessions(t *testing.T) {
	tracker := newPausedTracker(time.Minute)
	start := time.Date(2026, 8, 22, 20, 0, 0, 0, time.UTC)

	tracker.counts("a", true, 0, start)
	tracker.counts("b", true, 0, start)
	tracker.retain(map[string]bool{"b": true})

	tracker.mu.Lock()
	_, keptA := tracker.since["a"]
	_, keptB := tracker.since["b"]
	tracker.mu.Unlock()

	if keptA {
		t.Error("a session no longer present should have been dropped")
	}
	if !keptB {
		t.Error("a session still present must be kept, or its pause clock restarts")
	}
}

func TestPlexCountsPausedSessions(t *testing.T) {
	for _, state := range []string{"playing", "buffering", "paused"} {
		if !plexStateCounts(state) {
			t.Errorf("state %q should count as a live session", state)
		}
	}
	if plexStateCounts("stopped") {
		t.Error(`"stopped" ends a session and must not count`)
	}
}

// A dead client keeps reporting "playing" (Jellyfin holds the session
// open), so a frozen position expires like a long pause.
func TestStalledSessionStopsCounting(t *testing.T) {
	tracker := newPausedTracker(time.Minute)
	start := time.Now()

	if !tracker.counts("a", false, 10*time.Second, start) {
		t.Fatal("an advancing session must count")
	}
	if !tracker.counts("a", false, 20*time.Second, start.Add(30*time.Second)) {
		t.Fatal("still advancing, must still count")
	}

	// Frozen from here on.
	if !tracker.counts("a", false, 20*time.Second, start.Add(40*time.Second)) {
		t.Fatal("a freeze only just noticed must still count")
	}
	if tracker.counts("a", false, 20*time.Second, start.Add(2*time.Minute)) {
		t.Fatal("a session frozen past the timeout must stop counting")
	}

	if !tracker.counts("a", false, 45*time.Second, start.Add(3*time.Minute)) {
		t.Fatal("a resumed session must count again")
	}
}

// A server that never reports a position must not read as stalled.
func TestSessionWithoutPositionNeverStalls(t *testing.T) {
	tracker := newPausedTracker(time.Minute)
	start := time.Now()
	for _, after := range []time.Duration{0, time.Minute, time.Hour, 6 * time.Hour} {
		if !tracker.counts("b", false, 0, start.Add(after)) {
			t.Fatalf("a session with no position reporting must keep counting (after %s)", after)
		}
	}
}

func TestWatchersUseThePausedSessionTimeout(t *testing.T) {
	if got := newEmbyWatcher(flavorEmby, "http://x", "k", false).paused.timeout; got != pausedSessionTimeout {
		t.Errorf("Emby watcher timeout = %s", got)
	}
	if got := NewPlexWatcher("http://x", "t", false).paused.timeout; got != pausedSessionTimeout {
		t.Errorf("Plex watcher timeout = %s", got)
	}
	if pausedSessionTimeout != 30*time.Minute {
		t.Errorf("pausedSessionTimeout = %s, the README promises 30 minutes", pausedSessionTimeout)
	}
}
