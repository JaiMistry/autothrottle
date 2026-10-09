package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// reconnectMaxBackoff caps the wait between connection attempts. Also
// doubles as the "this connection was healthy" threshold: a connection
// that lasted longer than this gets a fresh backoff rather than
// inheriting the escalation from whatever went wrong before it.
const reconnectMaxBackoff = 30 * time.Second

// runWithBackoff drives a watcher's connect loop until ctx is done.
//
// Shared by every SessionWatcher because the reconnect policy is a
// property of the app's reliability posture, not of any one server's
// protocol: the live connection is the only input that detects
// playback promptly, so losing one silently is the failure mode that
// matters most, and every implementation should recover from it
// identically.
func runWithBackoff(ctx context.Context, label, baseURL string, tracker *connectionTracker, connect func(context.Context) error) {
	backoff := time.Second

	for {
		if ctx.Err() != nil {
			return
		}
		connectedAt := time.Now()
		tracker.markConnected(connectedAt)
		// Redacted here as well as where it is made: this is where every
		// watcher's error is logged and kept for the Dashboard.
		err := Redact(connect(ctx))
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Warn("watcher disconnected, reconnecting", "type", label, "url", baseURL, "error", err)
		}
		if time.Since(connectedAt) > reconnectMaxBackoff {
			backoff = time.Second // connection was healthy for a while; don't punish it for one drop
		}
		dropped := time.Now()
		tracker.markDisconnected(dropped, dropped.Add(backoff), err)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > reconnectMaxBackoff {
			backoff = reconnectMaxBackoff
		}
	}
}

// webSocketBase rewrites an http(s) base URL to its ws(s) equivalent,
// leaving anything else untouched.
func webSocketBase(baseURL string) string {
	switch {
	case strings.HasPrefix(baseURL, "https://"):
		return "wss://" + strings.TrimPrefix(baseURL, "https://")
	case strings.HasPrefix(baseURL, "http://"):
		return "ws://" + strings.TrimPrefix(baseURL, "http://")
	default:
		return baseURL
	}
}

// emit offers a session event to a watcher's channel, abandoning it if
// the watcher is shutting down. Identical for every protocol, so it
// lives here rather than once per watcher.
//
// The abandon-on-cancel matters: the channel is buffered, and a
// watcher whose consumer has already stopped would otherwise block
// here forever once that buffer filled, leaking the goroutine.
func emit(ctx context.Context, events chan<- SessionEvent, session Session, active bool) {
	select {
	case events <- SessionEvent{Session: session, Active: active}:
	case <-ctx.Done():
	}
}

// emitDetail reports that a session already known to be playing has
// moved — a new position, or a title that arrived late. It never
// changes whether the session exists, so it must not cause throttle
// state to be re-resolved.
func emitDetail(ctx context.Context, events chan<- SessionEvent, session Session) {
	select {
	case events <- SessionEvent{Session: session, Active: true, DetailOnly: true}:
	case <-ctx.Done():
	}
}

// getJSON performs an authenticated GET and decodes a JSON body into
// out. Every media server client needs exactly this shape — build,
// authenticate, send, check the status, decode, close — and writing it
// per call site is how a response body eventually goes unclosed.
//
// setAuth applies whatever the server expects, because they disagree:
// Plex takes a token header, Emby takes its own, Jellyfin takes an
// Authorization header it built itself.
func getJSON(ctx context.Context, url string, setAuth func(*http.Request), out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	setAuth(req)
	req.Header.Set("Accept", "application/json")

	resp, err := (&http.Client{Timeout: RequestTimeout}).Do(req)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
