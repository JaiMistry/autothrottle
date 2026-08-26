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
// The same cap covers a session that reports itself as playing while
// going nowhere — a killed client, a crashed player. Jellyfin holds
// such a session open indefinitely (measured: still "playing" four
// minutes after the client stopped talking), so without this one dead
// client throttles downloads until someone notices.
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
	// position is the last figure each session reported, so one that
	// has stopped moving can be told from one that is still running.
	position map[string]time.Duration
}

func newPausedTracker(timeout func() time.Duration) *pausedTracker {
	return &pausedTracker{timeout: timeout, since: make(map[string]time.Time), position: make(map[string]time.Duration)}
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
func (p *pausedTracker) counts(sessionKey string, paused bool, position time.Duration, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Not paused, and the position has moved: plainly still watching.
	// A server that never reports a position gives 0 every time, which
	// would look identical to a frozen one — so only a session that has
	// actually reported progress at least once can be judged stalled.
	if !paused {
		prev, seen := p.position[sessionKey]
		if !seen || position != prev || position == 0 {
			p.position[sessionKey] = position
			delete(p.since, sessionKey)
			return true
		}
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
	delete(p.position, sessionKey)
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
	for key := range p.position {
		if !present[key] {
			delete(p.position, key)
		}
	}
}
