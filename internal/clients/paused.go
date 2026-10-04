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

// PauseStore persists pause clocks for one media server so they
// survive a restart. Every method may fail; a tracker that cannot
// reach its store keeps working from memory alone, because losing
// throttling entirely is far worse than losing this.
type PauseStore interface {
	Load() (map[string]PausedSession, error)
	Save(sessionKey string, state PausedSession) error
	Delete(sessionKey string) error
	Retain(present map[string]bool) error
}

// pausedTracker decides whether a paused session still counts, shared
// by every watcher so they agree on what "still watching" means.
//
// Paused counts: the viewer is usually coming back, and releasing the
// limits for a two-minute pause hands the uplink to the torrents right
// up until playback resumes. But media servers hold a paused session
// open for hours, and Jellyfin keeps a dead client "playing" forever
// (measured), so a session that has stopped moving for longer than the
// timeout stops counting until it moves again.
type pausedTracker struct {
	timeout time.Duration

	// store, when set, makes the clocks durable. An abandoned-pause
	// timeout is measured in tens of minutes and a restart takes
	// seconds, so an in-memory clock alone means every update or reboot
	// resurrects a stream that had already been retired and throttles
	// for another full timeout with nobody watching.
	store    PauseStore
	hydrated bool

	mu    sync.Mutex
	since map[string]time.Time
	// position is the last figure each session reported, so one that
	// has stopped moving can be told from one that is still running.
	position map[string]time.Duration
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
		// The position comes back too, so a stream that was resumed
		// while the app was down is recognised as moving rather than
		// judged against a clock that no longer applies.
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

// counts reports whether a session that the server still lists should
// count toward throttling, given whether it is currently paused.
//
// Callers pass now rather than reading the clock here so a single pass
// over a session list judges every session against the same instant.
func (p *pausedTracker) counts(sessionKey string, paused bool, position time.Duration, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hydrate()

	// Not paused, and the position has moved: plainly still watching.
	// A server that never reports a position gives 0 every time, which
	// would look identical to a frozen one — so only a session that has
	// actually reported progress at least once can be judged stalled.
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

// retain drops every session not in present. Called from a snapshot,
// which is the only moment a watcher knows the full set of sessions
// that still exist — otherwise a long-lived watcher accumulates an
// entry for every session it has ever seen paused.
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
