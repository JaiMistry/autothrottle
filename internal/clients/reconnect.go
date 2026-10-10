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

// reconnectMaxBackoff caps the wait between attempts. A connection that
// lasted longer than this starts the next backoff afresh.
const reconnectMaxBackoff = 30 * time.Second

// runWithBackoff drives a watcher's connect loop until ctx is done,
// with one reconnect policy for every server.
func runWithBackoff(ctx context.Context, label, baseURL string, tracker *connectionTracker, connect func(context.Context) error) {
	backoff := time.Second

	for {
		if ctx.Err() != nil {
			return
		}
		// Redacted here too, where every watcher's error is logged.
		err := Redact(connect(ctx))
		lasted := tracker.dropped()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Warn("watcher disconnected, reconnecting", "type", label, "url", baseURL, "error", err)
		}
		if lasted > reconnectMaxBackoff {
			backoff = time.Second // it was healthy for a while; one drop is not a pattern
		}

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

// emit offers a session event, abandoning it once the watcher is
// shutting down, so a full buffer never blocks it forever.
func emit(ctx context.Context, events chan<- SessionEvent, session Session, active bool) {
	select {
	case events <- SessionEvent{Session: session, Active: active}:
	case <-ctx.Done():
	}
}

// emitDetail reports a known session's new position or late title. It
// never changes whether the session exists.
func emitDetail(ctx context.Context, events chan<- SessionEvent, session Session) {
	select {
	case events <- SessionEvent{Session: session, Active: true, DetailOnly: true}:
	case <-ctx.Done():
	}
}

// getJSON performs an authenticated GET and decodes the JSON body into
// out. setAuth applies each server's own credential header.
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
