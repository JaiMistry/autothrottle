package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/live"
)

// heard counts the changes the scheduler announces.
type heard struct {
	mu     sync.Mutex
	topics map[live.Topic]int
}

func (h *heard) Notify(t live.Topic) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.topics[t]++
}

func (h *heard) count(t live.Topic) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.topics[t]
}

func (h *heard) reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	clear(h.topics)
}

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

// Playback starting is announced at once, before the downloaders have
// answered, and again when the throttle lands and a period opens.
func TestSessionChangesAreAnnounced(t *testing.T) {
	s, database, limiter := newTestScheduler(t)
	h := &heard{topics: map[live.Topic]int{}}
	s.SetNotifier(h)
	msID, dlID := addServerAndDownloader(t, database)
	if _, err := database.CreateMapping(msID, dlID); err != nil {
		t.Fatal(err)
	}

	s.HandleSessionEvent(msID, clients.Session{Key: "a", Title: "Andor"}, true)
	if h.count(live.Dashboard) == 0 {
		t.Fatal("a session starting was not announced straight away")
	}
	eventually(t, "the throttle", func() bool { return len(limiter.snapshot()) == 1 && h.count(live.History) == 1 })
	if h.count(live.Dashboard) < 2 {
		t.Error("the throttle landing was not announced")
	}

	h.reset()
	s.HandleSessionEvent(msID, clients.Session{Key: "a"}, false)
	eventually(t, "the release", func() bool { return h.count(live.History) == 1 })

	h.reset()
	s.SetPaused(context.Background(), true)
	if h.count(live.Dashboard) == 0 {
		t.Error("turning throttling off was not announced")
	}
	h.reset()
	s.ClearSessions(msID)
	if h.count(live.Dashboard) == 0 {
		t.Error("clearing a server's sessions was not announced")
	}
}

// Sockets report position every second or so. Those reports are not
// news while they agree with the page's own clock; a seek, a pause or
// a new title is.
func TestDetailUpdatesAnnounceOnlyWhatShows(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	msID, _ := addServerAndDownloader(t, database)
	t0 := time.Now()
	s.HandleSessionEvent(msID, clients.Session{Key: "a", Title: "Andor", Position: time.Minute, PositionAt: t0}, true)
	h := &heard{topics: map[live.Topic]int{}}
	s.SetNotifier(h)

	s.UpdateSessionDetail(msID, clients.Session{Key: "a", Position: time.Minute + 5*time.Second, PositionAt: t0.Add(5 * time.Second)})
	if n := h.count(live.Dashboard); n != 0 {
		t.Errorf("steady playback was announced %d times", n)
	}
	s.UpdateSessionDetail(msID, clients.Session{Key: "a", Position: 20 * time.Minute, PositionAt: t0.Add(6 * time.Second)})
	if h.count(live.Dashboard) != 1 {
		t.Error("a seek was not announced")
	}
	s.UpdateSessionDetail(msID, clients.Session{Key: "a", Position: 20 * time.Minute, PositionAt: t0.Add(7 * time.Second), Paused: true})
	if h.count(live.Dashboard) != 2 {
		t.Error("a pause was not announced")
	}

	// A snapshot that matches is quiet; one that adds a session is not.
	h.reset()
	s.ReplaceSessions(msID, []clients.Session{{Key: "a", Title: "Andor", Position: 20 * time.Minute, PositionAt: t0.Add(30 * time.Second), Paused: true}})
	if n := h.count(live.Dashboard); n != 0 {
		t.Errorf("an unchanged snapshot was announced %d times", n)
	}
	s.ReplaceSessions(msID, []clients.Session{
		{Key: "a", Title: "Andor", Position: 20 * time.Minute, PositionAt: t0.Add(31 * time.Second), Paused: true},
		{Key: "b", Title: "Severance"},
	})
	if h.count(live.Dashboard) == 0 {
		t.Error("a snapshot with a new session was not announced")
	}
}

func TestDetailChanged(t *testing.T) {
	t0 := time.Now()
	base := clients.Session{Title: "Andor", Position: time.Minute, PositionAt: t0}
	at := func(pos, after time.Duration) clients.Session {
		s := base
		s.Position, s.PositionAt = pos, t0.Add(after)
		return s
	}
	paused := base
	paused.Paused = true
	cases := []struct {
		name string
		prev clients.Session
		next clients.Session
		want bool
	}{
		{"same reading", base, base, false},
		{"steady playback", base, at(time.Minute+10*time.Second, 10*time.Second), false},
		{"within the drift allowance", base, at(time.Minute+12*time.Second, 10*time.Second), false},
		{"jumped ahead", base, at(5*time.Minute, 10*time.Second), true},
		{"jumped back", base, at(10*time.Second, 10*time.Second), true},
		{"stalled", base, at(time.Minute, 10*time.Second), true},
		{"paused and still", paused, func() clients.Session { s := paused; s.PositionAt = t0.Add(time.Minute); return s }(), false},
		{"paused", base, paused, true},
		{"new title", base, func() clients.Session { s := base; s.Title = "Severance"; return s }(), true},
	}
	for _, c := range cases {
		if got := detailChanged(c.prev, c.next); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
