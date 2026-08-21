package clients

import (
	"context"
	"log/slog"
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
func runWithBackoff(ctx context.Context, label, baseURL string, connect func(context.Context) error) {
	backoff := time.Second

	for {
		if ctx.Err() != nil {
			return
		}
		connectedAt := time.Now()
		err := connect(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Warn("watcher disconnected, reconnecting", "type", label, "url", baseURL, "error", err)
		}
		if time.Since(connectedAt) > reconnectMaxBackoff {
			backoff = time.Second // connection was healthy for a while; don't punish it for one drop
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
