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

// detailTimeout bounds the one REST call made when a session starts,
// purely to learn what is playing. Short on purpose: if it does not
// answer quickly the reconciliation poll will supply the same detail
// anyway, and nothing about throttling is waiting on it.
const detailTimeout = 5 * time.Second

// PlexWatcher implements SessionWatcher against a Plex server's
// session-notification endpoint (free, undocumented-but-stable, no
// Plex Pass required), emitting SessionEvents on state changes.
type PlexWatcher struct {
	BaseURL           string
	Token             string
	SkipLocalPlayback bool

	events chan SessionEvent
	conn   connectionTracker
	paused *pausedTracker

	mu         sync.Mutex
	localCache map[string]bool // sessionKey -> is-local, cached while active
	// lastPos records the position most recently reported for a session,
	// which is also how the watcher knows it has already announced that
	// session at all. Cleared when the session ends, so the next
	// notification for that key announces it afresh.
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

// SetPauseStore makes this watcher's abandoned-pause clocks durable,
// so a restart does not resurrect a stream that was already retired.
// Call before Run; nil leaves the clocks in memory only.
func (w *PlexWatcher) SetPauseStore(store PauseStore) { w.paused.store = store }

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
			// ViewOffset is how far into the item the player is, in
			// milliseconds. Plex sends it on every playing notification,
			// which is what lets the timecode move between snapshots.
			ViewOffset int64 `json:"viewOffset"`
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
			delete(w.lastPos, n.SessionKey)
			w.mu.Unlock()
			w.paused.forget(n.SessionKey)
			emit(ctx, w.events, Session{Key: n.SessionKey}, false)
			continue
		}
		// A pause keeps counting, but not forever — see pausedTracker.
		//
		// The position matters here: the tracker treats a position of 0
		// as "moving" and resets its clock, so passing a literal 0 (as
		// this did before the notification's viewOffset was read) meant
		// the socket path could never retire a stalled session at all —
		// only the once-a-minute snapshot could.
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

		// The notification carries no title or device — the snapshot
		// supplies those — but it does carry the position, and that is
		// the part that has to keep up.
		//
		// Only the first notification for a session is a full event:
		// those re-resolve throttle state for every downloader, which is
		// an API call per client and must not fire on every tick of a
		// timecode. Movement afterwards goes out as detail, which
		// updates what the Dashboard draws without touching throttling.
		// A stop clears lastPos, so a session that ends and restarts
		// announces itself properly again.
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

		// The notification says something started, but not what it is —
		// identity lives only on the REST endpoint. Fetch it now instead
		// of leaving the Dashboard reading "1 session counting towards
		// throttling" with nothing beside it until the next
		// reconciliation poll, which can be a full minute away.
		go w.fillDetail(ctx, n.SessionKey)
	}
}

// fillDetail looks up what one session actually is and reports it as a
// detail update. Runs on its own goroutine: it makes a network call,
// and the socket read loop that triggered it must not wait for that.
//
// Failure is not worth surfacing — the reconciliation poll fills the
// same detail in shortly afterwards, so the cost of a miss is the
// blank row this exists to avoid, not a wrong one.
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
		})
	}
	w.paused.retain(present)

	// The snapshot is the freshest full view of what exists, so it's
	// also the right moment to drop locality entries for sessions that
	// have since ended — otherwise a long-lived watcher accumulates
	// them for every session it has ever seen.
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
