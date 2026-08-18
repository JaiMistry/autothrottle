// Package clients holds the HTTP clients for talking to media servers
// and downloaders. Connection-testing lives here now; the full
// event-driven WebSocket session clients (see plan.md's Real-time
// architecture section) build on top of this later.
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
