package clients

import (
	"sync"
	"time"
)

// ConnectionState is one watcher's live connection health, for the UI.
//
// The Dashboard says "WebSocket closed 41s ago — retrying in 30s"
// rather than a bare "Unreachable", because the two facts a reader
// actually wants are how long it has been down and whether anything is
// still trying. Both are known to the reconnect loop and nowhere else.
type ConnectionState struct {
	Connected bool
	// Since is when the current state began — the moment it connected,
	// or the moment it dropped.
	Since time.Time
	// RetryAt is when the next attempt is due, zero while connected.
	RetryAt time.Time
	// LastError is the reason for the most recent drop, verbatim.
	LastError string
}

// connectionTracker is embedded in each watcher and written only by its
// reconnect loop.
type connectionTracker struct {
	mu    sync.Mutex
	state ConnectionState
}

func (c *connectionTracker) Connection() ConnectionState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *connectionTracker) markConnected(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Only the transition resets the clock: a connection that has been
	// up for an hour should keep saying so.
	if !c.state.Connected {
		c.state = ConnectionState{Connected: true, Since: at}
	}
}

func (c *connectionTracker) markDisconnected(at, retryAt time.Time, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Connected || c.state.Since.IsZero() {
		c.state.Since = at
	}
	c.state.Connected = false
	c.state.RetryAt = retryAt
	if err != nil {
		c.state.LastError = err.Error()
	}
}
