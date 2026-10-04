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
// notification WebSocket.
//
// The protocol differs from Plex's in three ways that shape this file:
//
//  1. It is not event-driven. Plex pushes a notification when a
//     session changes state; these servers push nothing until asked,
//     and what they then push is the *entire session list* on a timer
//     (the SessionsStart subscription below). So state changes are
//     derived here, by diffing each pushed list against the previous
//     one, rather than being reported by the server.
//  2. It requires a keepalive. The server sends ForceKeepAlive naming
//     an interval and closes the connection if the client goes quiet
//     for longer — see startKeepAlive.
//  3. There is no locality flag, so the local-playback exemption is
//     computed from the client's address. See embySessionIsLocal.
type EmbyWatcher struct {
	BaseURL           string
	APIKey            string
	SkipLocalPlayback bool

	flavor embyFlavor
	events chan SessionEvent
	conn   connectionTracker
	paused *pausedTracker

	mu sync.Mutex
	// active is the last set this watcher reported as playing. It is
	// the baseline every pushed list is diffed against, and it
	// deliberately survives a reconnect: sessions that ended while the
	// connection was down are then reported as stopped by the first
	// list that arrives after it comes back.
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

// SetPauseStore makes this watcher's abandoned-pause clocks durable,
// so a restart does not resurrect a stream that was already retired.
// Call before Run; nil leaves the clocks in memory only.
func (w *EmbyWatcher) SetPauseStore(store PauseStore) { w.paused.store = store }

func (w *EmbyWatcher) Events() <-chan SessionEvent { return w.events }

func (w *EmbyWatcher) Run(ctx context.Context) {
	runWithBackoff(ctx, strings.ToLower(w.flavor.name), w.BaseURL, &w.conn, w.connectAndListen)
}

// embySessionPushInterval is the cadence requested from the server for
// pushed session lists, in milliseconds. Matches what these projects'
// own web clients ask for. It bounds how long after pressing play a
// throttle takes effect, so it is short; the payload is a small JSON
// array on a LAN, and the server is sending it to its own web UI
// anyway.
const embySessionPushInterval = 1500

// embyMessage is the envelope both directions of this protocol use.
type embyMessage struct {
	MessageType string          `json:"MessageType"`
	Data        json.RawMessage `json:"Data,omitempty"`
}

// embyConn wraps one live connection with the state that belongs to
// that connection rather than to the watcher.
type embyConn struct {
	c *websocket.Conn
	// mu serialises writes. coder/websocket permits only one writer at
	// a time, and the keepalive goroutine writes concurrently with the
	// subscribe call on the read path.
	mu sync.Mutex
	// keepAliveOnce guards against the server repeating ForceKeepAlive
	// on an already-live connection and spawning a second ticker.
	keepAliveOnce sync.Once
	// sawSessions records whether the subscription ever delivered
	// anything. See warnIfSubscriptionSilent.
	sawSessions atomic.Bool
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
	// Bound the keepalive goroutine to this connection rather than to
	// the caller's context, so a dropped connection stops it instead
	// of leaving one running per reconnect for the process's life.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	c, _, err := websocket.Dial(ctx, w.webSocketURL(), nil)
	if err != nil {
		return fmt.Errorf("dialing %s: %w", w.BaseURL, err)
	}
	defer func() { _ = c.CloseNow() }()

	conn := &embyConn{c: c}

	// Nothing arrives until this is sent: the server pushes session
	// lists only to clients that asked. "0,N" means start immediately,
	// repeat every N ms.
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

// embySubscriptionGrace is how long a connection may stay silent after
// subscribing before that silence is treated as a symptom. Generously
// longer than the push interval, so a slow or idle server is never
// mistaken for a broken subscription.
const embySubscriptionGrace = 30 * time.Second

// warnIfSubscriptionSilent logs the one failure that looks like
// success: a subscription the server accepts but never feeds (reported
// for Jellyfin with some API keys). Throttling still works off the
// reconciliation poll, only slower, so this makes that visible.
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

// startKeepAlive answers the server's ForceKeepAlive contract: stay
// audible or be disconnected. Sends at half the stated timeout so a
// single dropped message is not enough to trip it.
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
				// The read loop sees the same broken connection and
				// owns reconnecting; this goroutine just stops.
				return
			}
		}
	}
}

// handleSessions turns one pushed session list into the state changes
// since the previous list, which is what SessionWatcher's contract
// promises its consumers.
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
			// Moved, paused or resumed: pass it on so the Dashboard's
			// timecode tracks it. Not the title — a snapshot and a push
			// can disagree on it for the same session.
			emitDetail(ctx, w.events, session)
		}
	}
	for key, session := range previous {
		if _, still := next[key]; !still {
			emit(ctx, w.events, session, false)
		}
	}
}

// activeSet applies both filters — playback state and the
// local-playback exemption — to a raw session list. The single
// definition used by the WebSocket path and the snapshot path, so the
// two cannot disagree about what counts.
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
	// Every push is a full session list, so this is always the complete
	// picture of what still exists.
	w.paused.retain(present)
	return active
}

// Snapshot reads the session list over REST, applying the same
// filtering the event stream does.
//
// It also resets the diff baseline. The pushed lists are themselves
// full snapshots, so the two views converging is expected rather than
// notable — but if the socket is down, the REST read is the only
// current truth, and leaving a stale baseline behind would make the
// first push after reconnect emit changes that already happened.
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
