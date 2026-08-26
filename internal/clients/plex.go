// Package clients holds everything that talks to a media server or a
// downloader: the connection tests below, the live session watchers
// behind SessionWatcher, and the rate control behind RateLimiter.
//
// Each server's protocol quirks stay inside this package. Nothing
// downstream names a concrete client — the rest of the app resolves
// one of the interfaces in interfaces.go by type string.
package clients

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const testTimeout = 8 * time.Second

// TestPlex checks that baseURL reaches a real Plex server, and that
// token is accepted when Plex actually enforces it. Deliberately NOT
// "/identity" — that endpoint is unauthenticated by design (it
// responds even to an unclaimed server) and always 200s regardless of
// the token, which made an early version of this check a false
// positive.
//
// Confirmed directly against a real server: this is still a
// best-effort credential check, not a guarantee. Plex exempts
// requests from the local/private subnet from token enforcement by
// default ("local network" trust) — since AutoThrottle's whole
// deployment model is same-LAN, a wrong token can still legitimately
// come back 200 here. Full token validation happens for real when the
// WebSocket session client authenticates (a stricter path than a
// plain REST GET) — this check exists to catch a wrong URL or an
// unreachable server, not to be a hard credential gate.
// TestPlex checks a Plex server is reachable and answers.
//
// Worth knowing what this can and cannot establish: Plex ships with
// "allow unauthenticated access on the local network" on, so a LAN
// request is usually answered whatever the token says. A pass here
// means the server is reachable; the token is only really checked when
// the notification socket is opened. A wrong one therefore shows up as
// playback never being detected rather than as a failure here.
func TestPlex(ctx context.Context, baseURL, token string) error {
	baseURL = strings.TrimRight(baseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/", nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("X-Plex-Token", token)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: testTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return fmt.Errorf("server reachable but token was rejected (401)")
	default:
		return fmt.Errorf("server responded with unexpected status %d", resp.StatusCode)
	}
}
