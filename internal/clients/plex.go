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
	"errors"
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
// ErrPlexTokenUnverified reports that the server answered, but would
// have answered just the same with no token at all — so nothing about
// the credential was actually established.
//
// Plex ships with "allow unauthenticated access on the local network"
// on, and a LAN request then never has its token checked. Reporting
// that as "the credential was accepted" is a plain misstatement, and
// the first thing it costs is a wrong diagnosis when playback is never
// detected: the token is bad, the socket handshake rejects it, and the
// connection test said everything was fine.
var ErrPlexTokenUnverified = errors.New("plex token unverified")

// TestPlex checks a Plex server is reachable and, where the server
// allows it to be checked, that the token is good.
//
// The trick is to ask twice: once with the token and once without. If
// the unauthenticated request is refused, the server is enforcing and a
// success genuinely means the token worked. If it succeeds too, the
// server is not checking anything from here and the token is untested —
// which is reported rather than glossed over.
func TestPlex(ctx context.Context, baseURL, token string) error {
	baseURL = strings.TrimRight(baseURL, "/")

	probe := func(withToken bool) (int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/", nil)
		if err != nil {
			return 0, fmt.Errorf("building request: %w", err)
		}
		if withToken {
			req.Header.Set("X-Plex-Token", token)
		}
		req.Header.Set("Accept", "application/json")
		resp, err := (&http.Client{Timeout: testTimeout}).Do(req)
		if err != nil {
			return 0, fmt.Errorf("connecting to %s: %w", baseURL, err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode, nil
	}

	status, err := probe(true)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return fmt.Errorf("server reachable but token was rejected (401)")
	default:
		return fmt.Errorf("server responded with unexpected status %d", status)
	}

	// Reachable. Now: did the token have anything to do with it?
	anon, err := probe(false)
	if err != nil || anon == http.StatusOK {
		// A failure here says nothing about the token either way, so it
		// is treated the same as the server not enforcing.
		return ErrPlexTokenUnverified
	}
	return nil
}
