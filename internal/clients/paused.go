package clients

import (
	"sync"
	"time"
)

// defaultPausedSessionTimeout is how long a paused session keeps
// counting toward throttling when nothing configures otherwise.
const defaultPausedSessionTimeout = 30 * time.Minute

// pausedTracker decides whether a paused session still counts.
//
// A paused session is a session in progress: the viewer stepped away
// and is coming back, the client is often still buffering ahead, and
// releasing the download client's limits for those two minutes would
// hand the whole uplink to the torrents right up until the moment
// playback resumes. So paused counts, and there is no debounce timer
// anywhere — the server's own session list already says the stream
// exists.
//
// What that alone cannot handle is the viewer who pauses and never
// comes back. Media servers hold a paused session open for a long
// time, sometimes until the client app is killed, so "paused counts"
// taken literally means one forgotten client throttles downloads all
// night. This caps it: past the timeout, a session that has been
// continuously paused stops counting, and starts counting again the
// moment it resumes.
//
// Shared by every watcher so the two protocols cannot drift apart on
// what "still watching" means.
type pausedTracker struct {
	// timeout reads the configured limit at the point of use, so a
	// change takes effect without restarting a watcher. Nil, or any
	// non-positive value, means defaultPausedSessionTimeout.
	timeout func() time.Duration

	mu    sync.Mutex
	since map[string]time.Time
}

func newPausedTracker(timeout func() time.Duration) *pausedTracker {
	return &pausedTracker{timeout: timeout, since: make(map[string]time.Time)}
}

func (p *pausedTracker) limit() time.Duration {
	if p.timeout == nil {
		return defaultPausedSessionTimeout
	}
	if d := p.timeout(); d > 0 {
		return d
	}
	return defaultPausedSessionTimeout
}

// counts reports whether a session that the server still lists should
// count toward throttling, given whether it is currently paused.
//
// Callers pass now rather than reading the clock here so a single pass
// over a session list judges every session against the same instant.
func (p *pausedTracker) counts(sessionKey string, paused bool, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !paused {
		delete(p.since, sessionKey)
		return true
	}

	started, ok := p.since[sessionKey]
	if !ok {
		p.since[sessionKey] = now
		return true
	}
	return now.Sub(started) < p.limit()
}

// forget drops a session's pause state, for a session that has ended.
func (p *pausedTracker) forget(sessionKey string) {
	p.mu.Lock()
	delete(p.since, sessionKey)
	p.mu.Unlock()
}

// retain drops every session not in present. Called from a snapshot,
// which is the only moment a watcher knows the full set of sessions
// that still exist — otherwise a long-lived watcher accumulates an
// entry for every session it has ever seen paused.
func (p *pausedTracker) retain(present map[string]bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key := range p.since {
		if !present[key] {
			delete(p.since, key)
		}
	}
}
