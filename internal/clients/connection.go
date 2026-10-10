package clients

import "sync"

// ConnectionState is one watcher's live connection health.
type ConnectionState struct {
	Connected bool
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

func (c *connectionTracker) set(connected bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.Connected = connected
}
