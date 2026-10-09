// Package live tells open pages that something they show has changed.
//
// It carries no data, only which part of the app changed. A page that
// hears about a change fetches its own fragment exactly as it would on
// a timer, so there is one way to render each view rather than a second
// one for the stream, and holding a stream open never holds a database
// connection.
package live

import "sync"

// Topic names a part of the app a page can be showing.
type Topic string

const (
	// Dashboard is anything on the Dashboard's live block: sessions,
	// throttle state, connection health, the pause switch.
	Dashboard Topic = "dashboard"
	// History is a throttle period starting or ending.
	History Topic = "history"
	// Logs is an event written to, or cleared from, the log.
	Logs Topic = "logs"
)

// Notifier is what the parts that change state need: somewhere to say
// so. A nil Notifier is never called; see Notify.
type Notifier interface {
	Notify(Topic)
}

// Notify calls n.Notify, or does nothing when n is nil, so components
// built without a hub (tests, mostly) need no special case.
func Notify(n Notifier, t Topic) {
	if n != nil {
		n.Notify(t)
	}
}

// Hub fans changes out to every open stream.
type Hub struct {
	mu     sync.Mutex
	subs   map[*Subscription]struct{}
	closed bool
}

func NewHub() *Hub {
	return &Hub{subs: make(map[*Subscription]struct{})}
}

// Subscription is one open stream's view of the hub. Changes that land
// while the stream is busy writing collapse into one wake-up carrying
// every topic that changed, so a burst of events costs a burst of one.
type Subscription struct {
	hub     *Hub
	wake    chan struct{}
	done    chan struct{}
	mu      sync.Mutex
	pending map[Topic]bool
	once    sync.Once
}

// Subscribe opens a subscription. On a closed hub it comes back already
// done.
func (h *Hub) Subscribe() *Subscription {
	s := &Subscription{
		hub:     h,
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
		pending: make(map[Topic]bool),
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		s.once.Do(func() { close(s.done) })
		return s
	}
	h.subs[s] = struct{}{}
	return s
}

// Notify marks t changed for every subscription. It never blocks.
func (h *Hub) Notify(t Topic) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		s.mu.Lock()
		s.pending[t] = true
		s.mu.Unlock()
		select {
		case s.wake <- struct{}{}:
		default: // already due to wake; it will see t too
		}
	}
}

// Close ends every subscription, so open streams return and a server
// shutting down is not left waiting on them.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for s := range h.subs {
		s.once.Do(func() { close(s.done) })
		delete(h.subs, s)
	}
}

// Subscribers is how many streams are open.
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Wake fires when something changed since the last Take.
func (s *Subscription) Wake() <-chan struct{} { return s.wake }

// Done is closed when the subscription or its hub is closed.
func (s *Subscription) Done() <-chan struct{} { return s.done }

// Take returns the topics that changed since the last call, in a fixed
// order, and clears them.
func (s *Subscription) Take() []Topic {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Topic
	for _, t := range []Topic{Dashboard, History, Logs} {
		if s.pending[t] {
			out = append(out, t)
		}
	}
	clear(s.pending)
	return out
}

// Close ends this subscription.
func (s *Subscription) Close() {
	s.hub.mu.Lock()
	delete(s.hub.subs, s)
	s.hub.mu.Unlock()
	s.once.Do(func() { close(s.done) })
}
