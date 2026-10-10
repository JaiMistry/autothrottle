package clients

import (
	"log/slog"
	"sync"
	"time"
)

// pausedSessionTimeout is how long a continuously paused session keeps
// counting toward throttling before it is treated as abandoned.
const pausedSessionTimeout = 30 * time.Minute

// PausedSession is how long a session has been continuously paused,
// and where it was paused.
type PausedSession struct {
	Since    time.Time
	Position time.Duration
}

// PauseStore persists one media server's pause clocks across restarts.
// A tracker whose store fails keeps working from memory.
type PauseStore interface {
	Load() (map[string]PausedSession, error)
	Save(sessionKey string, state PausedSession) error
	Delete(sessionKey string) error
	Retain(present map[string]bool) error
}

// pausedTracker decides whether a paused session still counts, for
// every watcher. A pause counts, since the viewer is usually coming
// back; but servers hold paused sessions open for hours, and Jellyfin
// keeps a dead client "playing" forever, so a session that has not
// moved for the timeout stops counting until it moves again.
type pausedTracker struct {
	timeout time.Duration

	// store makes the clocks durable, so a restart does not revive a
	// retired stream for another full timeout.
	store    PauseStore
	hydrated bool

	mu       sync.Mutex
	since    map[string]time.Time
	position map[string]time.Duration // last reported, to spot a stall
}

func newPausedTracker(timeout time.Duration) *pausedTracker {
	return &pausedTracker{timeout: timeout, since: make(map[string]time.Time), position: make(map[string]time.Duration)}
}

// hydrate pulls persisted clocks in on first use. Caller holds mu.
func (p *pausedTracker) hydrate() {
	if p.hydrated || p.store == nil {
		return
	}
	p.hydrated = true
	saved, err := p.store.Load()
	if err != nil {
		slog.Warn("reading persisted pause state; abandoned-pause timers restart", "error", err)
		return
	}
	for key, state := range saved {
		p.since[key] = state.Since
		// So a stream resumed while the app was down reads as moving.
		p.position[key] = state.Position
	}
}

func (p *pausedTracker) persist(sessionKey string, state PausedSession) {
	if p.store == nil {
		return
	}
	if err := p.store.Save(sessionKey, state); err != nil {
		slog.Warn("recording pause state", "session", sessionKey, "error", err)
	}
}

func (p *pausedTracker) unpersist(sessionKey string) {
	if p.store == nil {
		return
	}
	if err := p.store.Delete(sessionKey); err != nil {
		slog.Warn("clearing pause state", "session", sessionKey, "error", err)
	}
}

// counts reports whether a session the server still lists counts
// toward throttling. now is passed in so one pass over a list judges
// every session against the same instant.
func (p *pausedTracker) counts(sessionKey string, paused bool, position time.Duration, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hydrate()

	// Playing and moving counts. A position of 0 may mean the server
	// never reports one, so only reported progress can be judged stalled.
	if !paused {
		prev, seen := p.position[sessionKey]
		if !seen || position != prev || position == 0 {
			p.position[sessionKey] = position
			if _, had := p.since[sessionKey]; had {
				delete(p.since, sessionKey)
				p.unpersist(sessionKey)
			}
			return true
		}
	}

	started, ok := p.since[sessionKey]
	if !ok {
		p.since[sessionKey] = now
		p.persist(sessionKey, PausedSession{Since: now, Position: position})
		return true
	}
	return now.Sub(started) < p.timeout
}

// forget drops a session's pause state, for a session that has ended.
func (p *pausedTracker) forget(sessionKey string) {
	p.mu.Lock()
	delete(p.since, sessionKey)
	delete(p.position, sessionKey)
	p.mu.Unlock()
	p.unpersist(sessionKey)
}

// retain drops every session not in present, from a snapshot: the one
// moment the full set of existing sessions is known.
func (p *pausedTracker) retain(present map[string]bool) {
	if p.store != nil {
		if err := p.store.Retain(present); err != nil {
			slog.Warn("pruning pause state", "error", err)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for key := range p.since {
		if !present[key] {
			delete(p.since, key)
		}
	}
	for key := range p.position {
		if !present[key] {
			delete(p.position, key)
		}
	}
}
