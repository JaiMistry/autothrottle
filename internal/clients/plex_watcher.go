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

	events chan SessionEvent

	mu         sync.Mutex
	localCache map[string]bool // sessionKey -> is-local, cached while active
}

func NewPlexWatcher(baseURL, token string, skipLocalPlayback bool) *PlexWatcher {
	return &PlexWatcher{
		BaseURL:           strings.TrimRight(baseURL, "/"),
		Token:             token,
		SkipLocalPlayback: skipLocalPlayback,
		events:            make(chan SessionEvent, 16),
		localCache:        make(map[string]bool),
	}
}

func (w *PlexWatcher) Events() <-chan SessionEvent { return w.events }

// Run connects and reconnects with backoff until ctx is cancelled. This
// isn't for detecting playback — it exists purely to notice a
// silently-dropped connection and re-establish it (plan.md's
// "reliability net": the event-driven design's one real failure mode).
func (w *PlexWatcher) Run(ctx context.Context) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}
		connectedAt := time.Now()
		err := w.connectAndListen(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Warn("plex watcher disconnected, reconnecting", "url", w.BaseURL, "error", err)
		}
		if time.Since(connectedAt) > maxBackoff {
			backoff = time.Second // connection was healthy for a while; don't punish it for one drop
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
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
		active := n.State == "playing" || n.State == "buffering"

		if !active {
			w.mu.Lock()
			delete(w.localCache, n.SessionKey)
			w.mu.Unlock()
			w.emit(ctx, n.SessionKey, false)
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

		w.emit(ctx, n.SessionKey, true)
	}
}

func (w *PlexWatcher) cachedLocal(sessionKey string) (bool, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	v, ok := w.localCache[sessionKey]
	return v, ok
}

func (w *PlexWatcher) emit(ctx context.Context, sessionKey string, active bool) {
	select {
	case w.events <- SessionEvent{SessionKey: sessionKey, Active: active}:
	case <-ctx.Done():
	}
}

// isSessionLocal cross-references /status/sessions for the given
// session's Player.local flag. The WebSocket "playing" notification
// itself carries no client-address info, so this REST call is the only
// way to resolve local-playback exemption.
func (w *PlexWatcher) isSessionLocal(ctx context.Context, sessionKey string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.BaseURL+"/status/sessions", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("X-Plex-Token", w.Token)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: testTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var out struct {
		MediaContainer struct {
			Metadata []struct {
				SessionKey string `json:"sessionKey"`
				Player     struct {
					Local bool `json:"local"`
				} `json:"Player"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	for _, m := range out.MediaContainer.Metadata {
		if m.SessionKey == sessionKey {
			return m.Player.Local, nil
		}
	}
	return false, fmt.Errorf("session %s not found in current sessions", sessionKey)
}

func plexWebSocketURL(baseURL, token string) string {
	wsURL := baseURL
	switch {
	case strings.HasPrefix(wsURL, "https://"):
		wsURL = "wss://" + strings.TrimPrefix(wsURL, "https://")
	case strings.HasPrefix(wsURL, "http://"):
		wsURL = "ws://" + strings.TrimPrefix(wsURL, "http://")
	}
	return wsURL + "/:/websockets/notifications?X-Plex-Token=" + url.QueryEscape(token)
}
