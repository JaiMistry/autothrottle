package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// EmbyWatcher implements SessionWatcher against Emby's and Jellyfin's
// notification WebSocket. Unlike Plex's, it pushes the whole session
// list on a timer once subscribed, so state changes are found by diffing
// lists; it closes a client that misses its keepalive; and it has no
// locality flag (see embySessionIsLocal).
type EmbyWatcher struct {
	BaseURL           string
	APIKey            string
	SkipLocalPlayback bool

	flavor embyFlavor
	events chan SessionEvent
	conn   connectionTracker
	paused *pausedTracker

	mu sync.Mutex
	// active is the baseline each pushed list is diffed against. It
	// survives a reconnect, so sessions that ended meanwhile are reported
	// stopped by the first list after.
	active map[string]Session
}

// NewEmbyWatcher builds a watcher for an Emby server. apiKey is a key
// created in Emby's dashboard, not a user password.
func NewEmbyWatcher(baseURL, apiKey string, skipLocalPlayback bool) *EmbyWatcher {
	return newEmbyWatcher(flavorEmby, baseURL, apiKey, skipLocalPlayback)
}

// NewJellyfinWatcher builds a watcher for a Jellyfin server. apiKey is
// a key created under Dashboard > API Keys.
func NewJellyfinWatcher(baseURL, apiKey string, skipLocalPlayback bool) *EmbyWatcher {
	return newEmbyWatcher(flavorJellyfin, baseURL, apiKey, skipLocalPlayback)
}

func newEmbyWatcher(f embyFlavor, baseURL, apiKey string, skipLocalPlayback bool) *EmbyWatcher {
	w := &EmbyWatcher{
		BaseURL:           strings.TrimRight(baseURL, "/"),
		APIKey:            apiKey,
		SkipLocalPlayback: skipLocalPlayback,
		flavor:            f,
		events:            make(chan SessionEvent, 16),
		active:            make(map[string]Session),
	}
	w.paused = newPausedTracker(pausedSessionTimeout)
	return w
}

// SetPauseStore makes the abandoned-pause clocks survive a restart.
// Call before Run; nil keeps them in memory.
func (w *EmbyWatcher) SetPauseStore(store PauseStore) { w.paused.store = store }

func (w *EmbyWatcher) Events() <-chan SessionEvent { return w.events }

func (w *EmbyWatcher) Run(ctx context.Context) {
	runWithBackoff(ctx, strings.ToLower(w.flavor.name), w.BaseURL, &w.conn, w.connectAndListen)
}

// embySessionPushInterval is the session-list cadence requested, in
// ms, as these servers' own web clients ask. It bounds how long a
// throttle takes after pressing play.
const embySessionPushInterval = 1500

// embyMessage is the envelope both directions of this protocol use.
type embyMessage struct {
	MessageType string          `json:"MessageType"`
	Data        json.RawMessage `json:"Data,omitempty"`
}

// embyConn is the state of one live connection.
type embyConn struct {
	c *websocket.Conn
	// mu serialises writes: coder/websocket allows one writer, and the
	// keepalive writes concurrently with the subscribe.
	mu sync.Mutex
	// keepAliveOnce stops a repeated ForceKeepAlive starting a second ticker.
	keepAliveOnce sync.Once
	sawSessions   atomic.Bool
}

func (e *embyConn) send(ctx context.Context, messageType string, data json.RawMessage) error {
	b, err := json.Marshal(embyMessage{MessageType: messageType, Data: data})
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.c.Write(ctx, websocket.MessageText, b)
}

func (w *EmbyWatcher) connectAndListen(ctx context.Context) error {
	// Scoped to this connection, so a drop stops its keepalive.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	c, _, err := websocket.Dial(ctx, w.webSocketURL(), nil)
	if err != nil {
		// The API key is in the URL, and so in the error.
		return fmt.Errorf("dialing %s: %w", w.BaseURL, Redact(err))
	}
	defer func() { _ = c.CloseNow() }()

	conn := &embyConn{c: c}

	// Nothing arrives until asked. "0,N": start now, repeat every N ms.
	subscribe := json.RawMessage(strconv.Quote(fmt.Sprintf("0,%d", embySessionPushInterval)))
	if err := conn.send(ctx, "SessionsStart", subscribe); err != nil {
		return fmt.Errorf("subscribing to sessions: %w", err)
	}
	go w.warnIfSubscriptionSilent(ctx, conn)

	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return fmt.Errorf("reading: %w", err)
		}
		w.handleMessage(ctx, conn, data)
	}
}

func (w *EmbyWatcher) handleMessage(ctx context.Context, conn *embyConn, data []byte) {
	var msg embyMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return // not an envelope we recognise; these servers send several message types
	}

	switch msg.MessageType {
	case "Sessions":
		var sessions []embySession
		if err := json.Unmarshal(msg.Data, &sessions); err != nil {
			slog.Warn("decoding session list", "type", w.flavor.name, "error", err)
			return
		}
		conn.sawSessions.Store(true)
		w.handleSessions(ctx, sessions)

	case "ForceKeepAlive":
		// Data is the server's timeout in seconds.
		var timeout int
		if err := json.Unmarshal(msg.Data, &timeout); err != nil || timeout <= 0 {
			timeout = 30
		}
		conn.keepAliveOnce.Do(func() {
			go w.startKeepAlive(ctx, conn, time.Duration(timeout)*time.Second)
		})
	}
}

// embySubscriptionGrace is how long a subscription may stay silent
// before it is reported, well past the push interval.
const embySubscriptionGrace = 30 * time.Second

// warnIfSubscriptionSilent logs a subscription the server accepts but
// never feeds (seen with some Jellyfin API keys). Throttling then runs,
// slower, off the reconciliation poll.
func (w *EmbyWatcher) warnIfSubscriptionSilent(ctx context.Context, conn *embyConn) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(embySubscriptionGrace):
	}
	if conn.sawSessions.Load() {
		return
	}
	slog.Warn("subscribed but received no session data; falling back to the reconciliation poll for playback detection",
		"type", w.flavor.name,
		"url", w.BaseURL,
		"hint", "if this persists, the API key may not grant session access on the notification socket")
}

// startKeepAlive answers ForceKeepAlive, at half the stated timeout so
// one lost message does not trip it.
func (w *EmbyWatcher) startKeepAlive(ctx context.Context, conn *embyConn, timeout time.Duration) {
	interval := timeout / 2
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := conn.send(ctx, "KeepAlive", nil); err != nil {
				return // the read loop owns reconnecting
			}
		}
	}
}

// handleSessions turns a pushed list into changes since the previous.
func (w *EmbyWatcher) handleSessions(ctx context.Context, sessions []embySession) {
	next := w.activeSet(sessions)

	w.mu.Lock()
	previous := w.active
	w.active = next
	w.mu.Unlock()

	for key, session := range next {
		if prev, had := previous[key]; !had {
			emit(ctx, w.events, session, true)
		} else if session.Position != prev.Position || session.Paused != prev.Paused {
			// Moved, paused or resumed. Not the title: a snapshot and a
			// push can disagree on it.
			emitDetail(ctx, w.events, session)
		}
	}
	for key, session := range previous {
		if _, still := next[key]; !still {
			emit(ctx, w.events, session, false)
		}
	}
}

// activeSet filters a raw list by playback state and the local
// exemption, for both the socket and snapshot paths.
func (w *EmbyWatcher) activeSet(sessions []embySession) map[string]Session {
	active := make(map[string]Session, len(sessions))
	present := make(map[string]bool, len(sessions))
	now := time.Now()
	for _, s := range sessions {
		if s.ID == "" {
			continue
		}
		present[s.ID] = true
		if !embyStateCounts(s) {
			continue
		}
		if w.SkipLocalPlayback && embySessionIsLocal(s.RemoteEndPoint) {
			continue
		}
		if !w.paused.counts(s.ID, s.PlayState.IsPaused, s.position(), now) {
			continue
		}
		active[s.ID] = Session{
			Key: s.ID, Title: s.label(), Device: s.DeviceName,
			Position: s.position(), PositionAt: time.Now(), Duration: s.duration(),
			Paused: s.PlayState.IsPaused,
			Local:  embySessionIsLocal(s.RemoteEndPoint),
		}
	}
	w.paused.retain(present)
	return active
}

// Snapshot reads the session list over REST, filtered as the stream is.
// It also resets the diff baseline, so the first push after a reconnect
// does not re-emit changes that already happened.
func (w *EmbyWatcher) Snapshot(ctx context.Context) ([]Session, error) {
	sessions, err := fetchEmbySessions(ctx, w.flavor, w.BaseURL, w.APIKey)
	if err != nil {
		return nil, err
	}

	set := w.activeSet(sessions)

	w.mu.Lock()
	w.active = set
	w.mu.Unlock()

	out := make([]Session, 0, len(set))
	for _, session := range set {
		out = append(out, session)
	}
	return out, nil
}

func (w *EmbyWatcher) webSocketURL() string {
	return webSocketBase(w.BaseURL) + w.flavor.webSocketPath +
		"?api_key=" + url.QueryEscape(w.APIKey) +
		"&deviceId=" + url.QueryEscape(embyDeviceID)
}

// Connection reports this watcher's live connection health.
func (w *EmbyWatcher) Connection() ConnectionState { return w.conn.Connection() }
