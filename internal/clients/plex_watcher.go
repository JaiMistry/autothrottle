package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// PlexWatcher implements SessionWatcher against a Plex server's
// session-notification endpoint (free, undocumented-but-stable, no
// Plex Pass required), emitting SessionEvents on state changes.
type PlexWatcher struct {
	BaseURL           string
	Token             string
	SkipLocalPlayback bool
	// PausedSessionTimeout caps how long a continuously paused session
	// keeps counting. Nil uses the built-in default.
	PausedSessionTimeout func() time.Duration

	events chan SessionEvent
	conn   connectionTracker
	paused *pausedTracker

	mu         sync.Mutex
	localCache map[string]bool // sessionKey -> is-local, cached while active
}

func NewPlexWatcher(baseURL, token string, skipLocalPlayback bool) *PlexWatcher {
	w := &PlexWatcher{
		BaseURL:           strings.TrimRight(baseURL, "/"),
		Token:             token,
		SkipLocalPlayback: skipLocalPlayback,
		events:            make(chan SessionEvent, 16),
		localCache:        make(map[string]bool),
	}
	w.paused = newPausedTracker(func() time.Duration {
		if w.PausedSessionTimeout == nil {
			return 0
		}
		return w.PausedSessionTimeout()
	})
	return w
}

func (w *PlexWatcher) Events() <-chan SessionEvent { return w.events }

// Run connects and reconnects with backoff until ctx is cancelled. This
// isn't for detecting playback — it exists purely to notice a
// silently-dropped connection and re-establish it — the one real
// failure mode an event-driven design has, since a dead socket looks
// exactly like a quiet one.
func (w *PlexWatcher) Run(ctx context.Context) {
	runWithBackoff(ctx, "plex", w.BaseURL, &w.conn, w.connectAndListen)
}

func (w *PlexWatcher) connectAndListen(ctx context.Context) error {
	wsURL := plexWebSocketURL(w.BaseURL, w.Token)

	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dialing %s: %w", w.BaseURL, err)
	}
	defer func() { _ = c.CloseNow() }()

	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return fmt.Errorf("reading: %w", err)
		}
		w.handleMessage(ctx, data)
	}
}

type plexNotificationEnvelope struct {
	NotificationContainer struct {
		Type                         string `json:"type"`
		PlaySessionStateNotification []struct {
			SessionKey string `json:"sessionKey"`
			State      string `json:"state"`
		} `json:"PlaySessionStateNotification"`
	} `json:"NotificationContainer"`
}

func (w *PlexWatcher) handleMessage(ctx context.Context, data []byte) {
	var env plexNotificationEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return // not JSON we care about (Plex sends several notification types on this socket)
	}
	if env.NotificationContainer.Type != "playing" {
		return
	}

	for _, n := range env.NotificationContainer.PlaySessionStateNotification {
		if n.SessionKey == "" {
			continue
		}
		if !plexStateCounts(n.State) {
			w.mu.Lock()
			delete(w.localCache, n.SessionKey)
			w.mu.Unlock()
			w.paused.forget(n.SessionKey)
			emit(ctx, w.events, Session{Key: n.SessionKey}, false)
			continue
		}
		// A pause keeps counting, but not forever — see pausedTracker.
		if !w.paused.counts(n.SessionKey, n.State == "paused", time.Now()) {
			emit(ctx, w.events, Session{Key: n.SessionKey}, false)
			continue
		}

		if w.SkipLocalPlayback {
			local, ok := w.cachedLocal(n.SessionKey)
			if !ok {
				var err error
				local, err = w.isSessionLocal(ctx, n.SessionKey)
				if err != nil {
					// Fail open toward throttling still working: if we
					// can't determine locality, don't risk silently
					// never throttling because a lookup failed — treat
					// as non-local so it counts.
					slog.Warn("checking session locality", "session", n.SessionKey, "error", err)
					local = false
				}
				w.mu.Lock()
				w.localCache[n.SessionKey] = local
				w.mu.Unlock()
			}
			if local {
				continue // exempted — never enters the active set at all
			}
		}

		// The notification carries no title or device, so this is the
		// key alone; the reconciliation snapshot fills the detail in on
		// its next pass.
		emit(ctx, w.events, Session{Key: n.SessionKey}, true)
	}
}

func (w *PlexWatcher) cachedLocal(sessionKey string) (bool, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	v, ok := w.localCache[sessionKey]
	return v, ok
}

// Snapshot lists the sessions Plex currently reports as playing, with
// local-playback exemption applied — the same filtering the event
// stream does, so the two are directly comparable. Used by the watcher
// manager to correct drift after a dropped connection.
func (w *PlexWatcher) Snapshot(ctx context.Context) ([]Session, error) {
	sessions, err := w.fetchSessions(ctx)
	if err != nil {
		return nil, err
	}

	present := make(map[string]bool, len(sessions))
	active := make([]Session, 0, len(sessions))
	now := time.Now()
	for _, s := range sessions {
		if s.SessionKey == "" {
			continue
		}
		present[s.SessionKey] = true
		if !plexStateCounts(s.Player.State) {
			continue
		}
		if w.SkipLocalPlayback && s.Player.Local {
			continue
		}
		// The snapshot is also where an abandoned pause is noticed:
		// the event stream sends one "paused" notification and then
		// nothing, so this poll is the only thing that re-evaluates it.
		if !w.paused.counts(s.SessionKey, s.Player.State == "paused", now) {
			continue
		}
		active = append(active, Session{
			Key: s.SessionKey, Title: s.label(), Device: s.device(),
			Position: time.Duration(s.ViewOffset) * time.Millisecond,
			Duration: time.Duration(s.Duration) * time.Millisecond,
			Local:    s.Player.Local,
		})
	}
	w.paused.retain(present)

	// The snapshot is the freshest full view of what exists, so it's
	// also the right moment to drop locality entries for sessions that
	// have since ended — otherwise a long-lived watcher accumulates
	// them for every session it has ever seen.
	w.mu.Lock()
	for key := range w.localCache {
		if !present[key] {
			delete(w.localCache, key)
		}
	}
	for _, s := range sessions {
		if s.SessionKey != "" {
			w.localCache[s.SessionKey] = s.Player.Local
		}
	}
	w.mu.Unlock()

	return active, nil
}

// plexSession is the subset of /status/sessions this app reads.
type plexSession struct {
	SessionKey string `json:"sessionKey"`
	// Title is the episode or film; GrandparentTitle is the show and
	// Index/ParentIndex the numbering, which together make the
	// "Andor S1E4" form the UI wants.
	Title            string `json:"title"`
	GrandparentTitle string `json:"grandparentTitle"`
	Index            int    `json:"index"`
	ParentIndex      int    `json:"parentIndex"`
	Type             string `json:"type"`
	// Both in milliseconds. Plex reports them on every session in
	// /status/sessions, so the progress bar needs no extra request.
	ViewOffset int64 `json:"viewOffset"`
	Duration   int64 `json:"duration"`
	Player     struct {
		Local  bool   `json:"local"`
		State  string `json:"state"`
		Title  string `json:"title"`
		Device string `json:"device"`
	} `json:"Player"`
}

// label renders what is playing the way Plex's own clients do: a show
// with its episode numbering, or just the title for anything else.
func (s plexSession) label() string {
	if s.Type == "episode" && s.GrandparentTitle != "" {
		if s.ParentIndex > 0 && s.Index > 0 {
			return fmt.Sprintf("%s S%dE%d", s.GrandparentTitle, s.ParentIndex, s.Index)
		}
		return s.GrandparentTitle
	}
	return s.Title
}

// device prefers the player's own name ("Living Room TV") over the
// hardware type ("Chromecast"), since that is what the owner named it.
func (s plexSession) device() string {
	if s.Player.Title != "" {
		return s.Player.Title
	}
	return s.Player.Device
}

// plexStateCounts reports which playback states mean a session still
// exists. Kept as one function so the WebSocket path and the
// reconciliation path can never disagree.
//
// "paused" counts: the stream is in progress and the viewer is coming
// back. Only "stopped" — and a session vanishing from the list — ends
// it. How long a paused session goes on counting is pausedTracker's
// job, not this one's.
func plexStateCounts(state string) bool {
	return state == "playing" || state == "buffering" || state == "paused"
}

// fetchSessions reads /status/sessions. The WebSocket "playing"
// notification carries no client-address or player-state detail, so
// this REST call backs both local-playback exemption and snapshot
// reconciliation.
func (w *PlexWatcher) fetchSessions(ctx context.Context) ([]plexSession, error) {
	var out struct {
		MediaContainer struct {
			Metadata []plexSession `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	err := getJSON(ctx, w.BaseURL+"/status/sessions", func(r *http.Request) {
		r.Header.Set("X-Plex-Token", w.Token)
	}, &out)
	if err != nil {
		return nil, err
	}
	return out.MediaContainer.Metadata, nil
}

// isSessionLocal cross-references /status/sessions for the given
// session's Player.local flag.
func (w *PlexWatcher) isSessionLocal(ctx context.Context, sessionKey string) (bool, error) {
	sessions, err := w.fetchSessions(ctx)
	if err != nil {
		return false, err
	}
	for _, s := range sessions {
		if s.SessionKey == sessionKey {
			return s.Player.Local, nil
		}
	}
	return false, fmt.Errorf("session %s not found in current sessions", sessionKey)
}

func plexWebSocketURL(baseURL, token string) string {
	return webSocketBase(baseURL) + "/:/websockets/notifications?X-Plex-Token=" + url.QueryEscape(token)
}

// Connection reports this watcher's live connection health.
func (w *PlexWatcher) Connection() ConnectionState { return w.conn.Connection() }
