package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
)

const testRestoreDelay = 300 * time.Millisecond

// throttledThenEnded is a downloader throttled by one session, which
// has just ended, under a short restore delay.
func throttledThenEnded(t *testing.T) (*Scheduler, *fakeLimiter, int64, int64) {
	t.Helper()
	s, database, limiter := newTestScheduler(t)
	s.SetRestoreDelay(testRestoreDelay)
	msID, dlID := addServerAndDownloader(t, database)
	if _, err := database.CreateMapping(msID, dlID); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s.HandleSessionEvent(msID, clients.Session{Key: "episode-1"}, true)
	s.resolve(ctx)
	if last(limiter).download != 500 {
		t.Fatalf("not throttled: %+v", limiter.snapshot())
	}
	s.HandleSessionEvent(msID, clients.Session{Key: "episode-1"}, false)
	s.resolve(ctx)
	return s, limiter, msID, dlID
}

func last(l *fakeLimiter) call {
	calls := l.snapshot()
	if len(calls) == 0 {
		return call{download: -1}
	}
	return calls[len(calls)-1]
}

func restoredAny(l *fakeLimiter) bool {
	throttled := false
	for _, c := range l.snapshot() {
		if c.download == 500 {
			throttled = true
		} else if throttled && c.download == 0 {
			return true
		}
	}
	return false
}

// The throttle outlasts the playback briefly, then lifts by itself.
func TestPlaybackEndingHoldsTheThrottleBriefly(t *testing.T) {
	s, limiter, _, dlID := throttledThenEnded(t)
	if restoredAny(limiter) {
		t.Fatalf("restored the moment playback ended: %+v", limiter.snapshot())
	}
	if !s.ReleasePending() || !s.State(dlID).Throttled {
		t.Error("the hold isn't reported while it lasts")
	}
	eventually(t, "the release", func() bool { return last(limiter).download == 0 })
	if s.ReleasePending() || s.State(dlID).Throttled {
		t.Error("still reported as releasing after the release")
	}
}

// Autoplay's next episode, within the delay, keeps the throttle on: the
// downloader is never restored in between.
func TestNextEpisodeKeepsTheThrottle(t *testing.T) {
	s, limiter, msID, _ := throttledThenEnded(t)
	s.HandleSessionEvent(msID, clients.Session{Key: "episode-2"}, true)
	s.resolve(context.Background())
	if s.ReleasePending() {
		t.Error("the next episode didn't cancel the release")
	}
	time.Sleep(3 * testRestoreDelay)
	if restoredAny(limiter) {
		t.Errorf("restored between episodes: %+v", limiter.snapshot())
	}
}

// Turning throttling off is the user's call: it doesn't wait.
func TestTurningThrottlingOffDuringTheHoldRestoresAtOnce(t *testing.T) {
	s, limiter, _, _ := throttledThenEnded(t)
	s.SetPaused(context.Background(), true)
	if last(limiter).download != 0 || s.ReleasePending() {
		t.Errorf("pausing waited for the hold: %+v", limiter.snapshot())
	}
}

// So is switching the pairing off, during playback or the hold.
func TestDisablingTheMappingRestoresAtOnce(t *testing.T) {
	s, limiter, msID, dlID := throttledThenEnded(t)
	if err := s.db.SetMappingForPair(msID, dlID, false); err != nil {
		t.Fatal(err)
	}
	s.resolve(context.Background())
	if last(limiter).download != 0 || s.ReleasePending() {
		t.Errorf("a disabled mapping waited for the hold: %+v", limiter.snapshot())
	}
}

// A downloader deleted during the hold leaves nothing pending.
func TestDeletingTheDownloaderDropsItsHold(t *testing.T) {
	s, _, _, dlID := throttledThenEnded(t)
	if err := s.db.DeleteDownloader(dlID); err != nil {
		t.Fatal(err)
	}
	s.resolve(context.Background())
	if s.ReleasePending() {
		t.Error("a deleted downloader is still releasing")
	}
}
