package clients

import (
	"testing"
	"time"
)

func TestPausedTrackerCountsUntilTimeout(t *testing.T) {
	tracker := newPausedTracker(func() time.Duration { return time.Minute })
	start := time.Date(2026, 8, 22, 20, 0, 0, 0, time.UTC)

	if !tracker.counts("a", true, start) {
		t.Error("a session that has just paused must still count")
	}
	if !tracker.counts("a", true, start.Add(59*time.Second)) {
		t.Error("still within the timeout, so it must still count")
	}
	if tracker.counts("a", true, start.Add(61*time.Second)) {
		t.Error("past the timeout, an abandoned pause must stop counting")
	}
}

// The clock starts when the pause starts, not when it is first
// observed a second time — and resuming resets it, so someone who
// pauses briefly every few minutes never accumulates toward the cap.
func TestPausedTrackerResetsOnResume(t *testing.T) {
	tracker := newPausedTracker(func() time.Duration { return time.Minute })
	start := time.Date(2026, 8, 22, 20, 0, 0, 0, time.UTC)

	tracker.counts("a", true, start)
	if !tracker.counts("a", false, start.Add(30*time.Second)) {
		t.Fatal("a playing session always counts")
	}
	// Paused again: the timeout restarts from here.
	if !tracker.counts("a", true, start.Add(31*time.Second)) {
		t.Error("a fresh pause must count")
	}
	if !tracker.counts("a", true, start.Add(80*time.Second)) {
		t.Error("the timeout should have restarted on resume, not carried over")
	}
}

func TestPausedTrackerPlayingAlwaysCounts(t *testing.T) {
	tracker := newPausedTracker(func() time.Duration { return time.Nanosecond })
	now := time.Now()
	for i := 0; i < 3; i++ {
		if !tracker.counts("a", false, now.Add(time.Duration(i)*time.Hour)) {
			t.Fatal("a session that is not paused must always count")
		}
	}
}

func TestPausedTrackerForgetsEndedSessions(t *testing.T) {
	tracker := newPausedTracker(func() time.Duration { return time.Minute })
	start := time.Date(2026, 8, 22, 20, 0, 0, 0, time.UTC)

	tracker.counts("a", true, start)
	tracker.forget("a")
	// Forgotten, so the pause clock starts over rather than being
	// judged against the original pause.
	if !tracker.counts("a", true, start.Add(2*time.Minute)) {
		t.Error("a forgotten session must start its pause clock fresh")
	}
}

// A long-lived watcher would otherwise keep an entry for every session
// it has ever seen paused.
func TestPausedTrackerRetainDropsAbsentSessions(t *testing.T) {
	tracker := newPausedTracker(func() time.Duration { return time.Minute })
	start := time.Date(2026, 8, 22, 20, 0, 0, 0, time.UTC)

	tracker.counts("a", true, start)
	tracker.counts("b", true, start)
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

func TestPausedTrackerFallsBackToDefaultTimeout(t *testing.T) {
	for name, tracker := range map[string]*pausedTracker{
		"nil accessor":  newPausedTracker(nil),
		"zero duration": newPausedTracker(func() time.Duration { return 0 }),
		"negative":      newPausedTracker(func() time.Duration { return -time.Hour }),
	} {
		if got := tracker.limit(); got != defaultPausedSessionTimeout {
			t.Errorf("%s: limit = %s, want the default %s", name, got, defaultPausedSessionTimeout)
		}
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
