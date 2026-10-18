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

// detailTimeout bounds the lookup of what a new session is playing.
// Short: the reconciliation poll supplies the same detail anyway.
const detailTimeout = 5 * time.Second

// PlexWatcher implements SessionWatcher against Plex's notification
// socket (undocumented but stable; no Plex Pass needed).
type PlexWatcher struct {
	BaseURL           string
	Token             string
	SkipLocalPlayback bool

	events chan SessionEvent
	conn   connectionTracker
	paused *pausedTracker

	mu         sync.Mutex
	localCache map[string]bool // sessionKey -> is-local, cached while active
	// lastPos is each announced session's last position; a session not
	// in it has not been announced yet.
	lastPos map[string]time.Duration
}

func NewPlexWatcher(baseURL, token string, skipLocalPlayback bool) *PlexWatcher {
	w := &PlexWatcher{
		BaseURL:           strings.TrimRight(baseURL, "/"),
		Token:             token,
		SkipLocalPlayback: skipLocalPlayback,
		events:            make(chan SessionEvent, 16),
		localCache:        make(map[string]bool),
		lastPos:           make(map[string]time.Duration),
	}
	w.paused = newPausedTracker(pausedSessionTimeout)
	return w
}

// SetPauseStore makes the abandoned-pause clocks survive a restart.
// Call before Run; nil keeps them in memory.
func (w *PlexWatcher) SetPauseStore(store PauseStore) { w.paused.store = store }

func (w *PlexWatcher) Events() <-chan SessionEvent { return w.events }

// Run connects, and reconnects with backoff, until ctx is cancelled.
func (w *PlexWatcher) Run(ctx context.Context) {
	runWithBackoff(ctx, "plex", w.BaseURL, &w.conn, w.connectAndListen)
}

func (w *PlexWatcher) connectAndListen(ctx context.Context) error {
	wsURL := plexWebSocketURL(w.BaseURL, w.Token)

	c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: newHTTPClient(0)})
	if err != nil {
		// The token is in the URL, and so in the error.
		return fmt.Errorf("dialing %s: %w", w.BaseURL, Redact(err))
	}
	defer func() { _ = c.CloseNow() }()
	w.conn.connected()

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
			ViewOffset int64  `json:"viewOffset"` // ms
		} `json:"PlaySessionStateNotification"`
	} `json:"NotificationContainer"`
}

func (w *PlexWatcher) handleMessage(ctx context.Context, data []byte) {
	var env plexNotificationEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return // Plex sends several message types on this socket
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
			delete(w.lastPos, n.SessionKey)
			w.mu.Unlock()
			w.paused.forget(n.SessionKey)
			emit(ctx, w.events, Session{Key: n.SessionKey}, false)
			continue
		}
		// A pause counts, but not forever (pausedTracker), which reads a
		// moving position as still playing.
		if !w.paused.counts(n.SessionKey, n.State == "paused", time.Duration(n.ViewOffset)*time.Millisecond, time.Now()) {
			w.mu.Lock()
			delete(w.lastPos, n.SessionKey)
			w.mu.Unlock()
			emit(ctx, w.events, Session{Key: n.SessionKey}, false)
			continue
		}

		if w.SkipLocalPlayback {
			local, ok := w.cachedLocal(n.SessionKey)
			if !ok {
				var err error
				local, err = w.isSessionLocal(ctx, n.SessionKey)
				if err != nil {
					// Unknown counts as non-local: a failed lookup must
					// not silently stop throttling.
					slog.Warn("checking session locality", "session", n.SessionKey, "error", err)
					local = false
				}
				w.mu.Lock()
				w.localCache[n.SessionKey] = local
				w.mu.Unlock()
			}
			if local {
				continue // exempt
			}
		}

		// Only a session's first notification is a full event, which
		// re-resolves throttling; later ones carry position only, as
		// detail, so a moving timecode costs no downloader API calls.
		update := Session{
			Key:        n.SessionKey,
			Position:   time.Duration(n.ViewOffset) * time.Millisecond,
			PositionAt: time.Now(),
			Paused:     n.State == "paused",
		}
		w.mu.Lock()
		_, announced := w.lastPos[n.SessionKey]
		w.lastPos[n.SessionKey] = update.Position
		w.mu.Unlock()

		if announced {
			emitDetail(ctx, w.events, update)
			continue
		}
		emit(ctx, w.events, update, true)

		// The notification has no title or device; fetch them now rather
		// than wait up to a minute for the reconciliation poll.
		go w.fillDetail(ctx, n.SessionKey)
	}
}

// fillDetail reports what one session is playing, as a detail update.
// A failure is ignored: the reconciliation poll fills it in shortly.
func (w *PlexWatcher) fillDetail(ctx context.Context, sessionKey string) {
	ctx, cancel := context.WithTimeout(ctx, detailTimeout)
	defer cancel()

	sessions, err := w.fetchSessions(ctx)
	if err != nil {
		slog.Debug("plex: fetching session detail", "session", sessionKey, "error", err)
		return
	}
	for _, s := range sessions {
		if s.SessionKey != sessionKey {
			continue
		}
		emitDetail(ctx, w.events, Session{
			Key:        s.SessionKey,
			Title:      s.label(),
			Device:     s.device(),
			Position:   time.Duration(s.ViewOffset) * time.Millisecond,
			PositionAt: time.Now(),
			Duration:   time.Duration(s.Duration) * time.Millisecond,
			Paused:     s.Player.State == "paused",
			Local:      s.Player.Local,
			Episode:    s.Type == "episode",
		})
		return
	}
}

func (w *PlexWatcher) cachedLocal(sessionKey string) (bool, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	v, ok := w.localCache[sessionKey]
	return v, ok
}

// Snapshot lists the sessions Plex reports as playing, filtered as the
// event stream is.
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
		// Only this poll notices an abandoned pause: the stream sends one
		// "paused" notification and then nothing.
		if !w.paused.counts(s.SessionKey, s.Player.State == "paused", time.Duration(s.ViewOffset)*time.Millisecond, now) {
			continue
		}
		active = append(active, Session{
			Key: s.SessionKey, Title: s.label(), Device: s.device(),
			Position:   time.Duration(s.ViewOffset) * time.Millisecond,
			PositionAt: time.Now(),
			Duration:   time.Duration(s.Duration) * time.Millisecond,
			Paused:     s.Player.State == "paused",
			Local:      s.Player.Local,
			Episode:    s.Type == "episode",
		})
	}
	w.paused.retain(present)

	// Drop entries for sessions that have ended, so they don't pile up.
	w.mu.Lock()
	for key := range w.lastPos {
		if !present[key] {
			delete(w.lastPos, key)
		}
	}
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
	// Title is the episode or film; the show and its numbering make
	// "Andor S1E4".
	Title            string `json:"title"`
	GrandparentTitle string `json:"grandparentTitle"`
	Index            int    `json:"index"`
	ParentIndex      int    `json:"parentIndex"`
	Type             string `json:"type"`
	// Both in milliseconds.
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

// device prefers the player's name ("Living Room TV") to its hardware
// type ("Chromecast").
func (s plexSession) device() string {
	if s.Player.Title != "" {
		return s.Player.Title
	}
	return s.Player.Device
}

// plexStateCounts says which states mean a session exists, for both the
// socket and snapshot paths. Paused counts; for how long is
// pausedTracker's job.
func plexStateCounts(state string) bool {
	return state == "playing" || state == "buffering" || state == "paused"
}

// fetchSessions reads /status/sessions, which has what notifications
// lack: locality and player detail.
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
