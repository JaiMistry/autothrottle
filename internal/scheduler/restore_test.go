package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/db"
)

const testRestoreDelay = 300 * time.Millisecond

// An episode two minutes from the end of its 45 minutes: credits, or
// autoplay's countdown.
func endingEpisode(key string) clients.Session {
	return clients.Session{Key: key, Episode: true, Duration: 45 * time.Minute,
		Position: 43 * time.Minute, PositionAt: time.Now()}
}

// throttledThenEnded is a downloader throttled by one session, which has
// just ended, under a short restore delay.
func throttledThenEnded(t *testing.T, session clients.Session) (*Scheduler, *db.DB, *fakeLimiter, int64, int64) {
	t.Helper()
	s, database, limiter := newTestScheduler(t)
	s.SetRestoreDelay(testRestoreDelay)
	msID, dlID := addServerAndDownloader(t, database)
	if _, err := database.CreateMapping(msID, dlID); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s.HandleSessionEvent(msID, session, true)
	s.resolve(ctx)
	if last(limiter).download != 500 {
		t.Fatalf("not throttled: %+v", limiter.snapshot())
	}
	s.HandleSessionEvent(msID, clients.Session{Key: session.Key}, false)
	s.resolve(ctx)
	return s, database, limiter, msID, dlID
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

// An episode ending near its end holds the throttle briefly, then it
// lifts by itself.
func TestEpisodeEndingHoldsTheThrottleBriefly(t *testing.T) {
	s, _, limiter, _, dlID := throttledThenEnded(t, endingEpisode("episode-1"))
	if restoredAny(limiter) {
		t.Fatalf("restored the moment the episode ended: %+v", limiter.snapshot())
	}
	if !s.ReleasePending() || !s.State(dlID).Throttled {
		t.Error("the hold isn't reported while it lasts")
	}
	eventually(t, "the release", func() bool { return last(limiter).download == 0 })
	if s.ReleasePending() || s.State(dlID).Throttled {
		t.Error("still reported as releasing after the release")
	}
}

// Anything else restores the moment it ends: a movie, an episode left
// midway, one paused at the end (as an abandoned pause ends), or an item
// of unknown length.
func TestOnlyAnEpisodeNearItsEndHolds(t *testing.T) {
	midway := endingEpisode("e")
	midway.Position = 20 * time.Minute
	paused := endingEpisode("e")
	paused.Paused = true
	movie := endingEpisode("m")
	movie.Episode = false
	unknown := endingEpisode("e")
	unknown.Duration = 0
	for name, session := range map[string]clients.Session{
		"a movie": movie, "an episode left midway": midway, "a paused episode": paused, "an unknown length": unknown,
	} {
		s, _, limiter, _, _ := throttledThenEnded(t, session)
		if last(limiter).download != 0 || s.ReleasePending() {
			t.Errorf("%s held the throttle: %+v", name, limiter.snapshot())
		}
	}
}

// The details a snapshot replaced are what decide, when it is the
// snapshot that finds the episode gone.
func TestASnapshotFindingTheEpisodeGoneHolds(t *testing.T) {
	s, database, limiter := newTestScheduler(t)
	s.SetRestoreDelay(testRestoreDelay)
	msID, dlID := addServerAndDownloader(t, database)
	if _, err := database.CreateMapping(msID, dlID); err != nil {
		t.Fatal(err)
	}
	s.ReplaceSessions(msID, []clients.Session{endingEpisode("episode-1")})
	eventually(t, "the throttle", func() bool { return last(limiter).download == 500 })
	s.ReplaceSessions(msID, nil)
	s.resolve(context.Background())
	if restoredAny(limiter) || !s.ReleasePending() {
		t.Errorf("the snapshot's end didn't hold: %+v", limiter.snapshot())
	}
}

// Autoplay's next episode, within the delay, keeps the throttle on: the
// downloader is never restored in between.
func TestNextEpisodeKeepsTheThrottle(t *testing.T) {
	s, _, limiter, msID, _ := throttledThenEnded(t, endingEpisode("episode-1"))
	s.HandleSessionEvent(msID, clients.Session{Key: "episode-2", Episode: true}, true)
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
	s, _, limiter, _, _ := throttledThenEnded(t, endingEpisode("episode-1"))
	s.SetPaused(context.Background(), true)
	if last(limiter).download != 0 || s.ReleasePending() {
		t.Errorf("pausing waited for the hold: %+v", limiter.snapshot())
	}
}

// So is switching the pairing off.
func TestDisablingTheMappingRestoresAtOnce(t *testing.T) {
	s, database, limiter, msID, dlID := throttledThenEnded(t, endingEpisode("episode-1"))
	if err := database.SetMappingForPair(msID, dlID, false); err != nil {
		t.Fatal(err)
	}
	s.resolve(context.Background())
	if last(limiter).download != 0 || s.ReleasePending() {
		t.Errorf("a disabled mapping waited for the hold: %+v", limiter.snapshot())
	}
}

// A downloader deleted during the hold leaves nothing pending.
func TestDeletingTheDownloaderDropsItsHold(t *testing.T) {
	s, database, _, _, dlID := throttledThenEnded(t, endingEpisode("episode-1"))
	if err := database.DeleteDownloader(dlID); err != nil {
		t.Fatal(err)
	}
	s.resolve(context.Background())
	if s.ReleasePending() {
		t.Error("a deleted downloader is still releasing")
	}
}

func TestNextEpisodeLikely(t *testing.T) {
	now := time.Now()
	ep := func(duration, position time.Duration, reportedAgo time.Duration) clients.Session {
		return clients.Session{Episode: true, Duration: duration, Position: position, PositionAt: now.Add(-reportedAgo)}
	}
	cases := []struct {
		name    string
		session clients.Session
		want    bool
	}{
		{"credits of a half-hour episode", ep(22*time.Minute, 20*time.Minute, 0), true},
		{"the middle of one", ep(22*time.Minute, 10*time.Minute, 0), false},
		{"reported midway, but that was long enough ago", ep(22*time.Minute, 15*time.Minute, 3*time.Minute), true},
		{"the last tenth of a long episode, beyond five minutes", ep(90*time.Minute, 83*time.Minute, 0), true},
		{"before the last tenth of a long one", ep(90*time.Minute, 75*time.Minute, 0), false},
		{"past its reported end", ep(10*time.Minute, 11*time.Minute, 0), true},
		{"a movie at its credits", clients.Session{Duration: time.Hour, Position: 59 * time.Minute, PositionAt: now}, false},
	}
	for _, c := range cases {
		if got := nextEpisodeLikely(c.session, now); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
