package clients

import (
	"sync"
	"time"
)

// ConnectionState is one watcher's live connection health.
type ConnectionState struct {
	Connected bool
}

// connectionTracker is embedded in each watcher and written only by its
// reconnect loop.
type connectionTracker struct {
	mu    sync.Mutex
	state ConnectionState
	since time.Time // when the current connection was made; zero while down
}

func (c *connectionTracker) Connection() ConnectionState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// connected is called by a watcher once its socket is actually open —
// not before the attempt, or a server that refuses every dial would read
// as connected between its failures.
func (c *connectionTracker) connected() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.Connected = true
	c.since = time.Now()
}

// dropped marks the connection down and says how long it had been up,
// zero if it never came up at all.
func (c *connectionTracker) dropped() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var lasted time.Duration
	if c.state.Connected {
		lasted = time.Since(c.since)
	}
	c.state.Connected = false
	c.since = time.Time{}
	return lasted
}
