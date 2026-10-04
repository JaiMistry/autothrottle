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

// RequestTimeout bounds each request a connection test makes.
const RequestTimeout = 8 * time.Second

// TestPlex checks a Plex server answers, then checks the token with
// plex.tv, since the server itself accepts any token on the LAN.
func TestPlex(ctx context.Context, baseURL, token string) error {
	baseURL = strings.TrimRight(baseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/", nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("X-Plex-Token", token)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: RequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		// Reachable. That is all this proves — see verifyPlexToken.
	case http.StatusUnauthorized:
		return fmt.Errorf("server reachable but token was rejected (401)")
	default:
		return fmt.Errorf("server responded with unexpected status %d", resp.StatusCode)
	}

	return verifyPlexToken(ctx, token)
}

// plexAccountURL is the only endpoint that will actually tell us whether
// a token is real.
const plexAccountURL = "https://plex.tv/api/v2/user"

// verifyPlexToken checks the token against plex.tv.
//
// A Plex server exempts the local network from token enforcement by
// default, so every request above answers 200 whether the token is
// right, wrong, or absent entirely — measured against a real server:
// no token at all still returns 200. Testing only the server therefore
// reports "connected" for a token that will never work, and the failure
// surfaces much later as a session feed that connects and then says
// nothing.
//
// plex.tv does enforce it: 401 for a bad token, 200 for a good one.
func verifyPlexToken(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, plexAccountURL, nil)
	if err != nil {
		return fmt.Errorf("building token check: %w", err)
	}
	// plex.tv's v2 API rejects requests without a client identifier, and
	// the same headers the sign-in flow sends work here. The identifier
	// need not be the stable per-install one: nothing is being claimed,
	// only a token read back.
	plexAuthHeaders(req, plexProduct)
	req.Header.Set("X-Plex-Token", token)

	resp, err := (&http.Client{Timeout: RequestTimeout}).Do(req)
	if err != nil {
		// Deliberately a failure rather than a pass. The server answered,
		// but the one thing that can vouch for the token did not, and
		// saying "connected" here is exactly the false assurance this
		// check exists to remove.
		return fmt.Errorf("server is reachable, but the token could not be checked with plex.tv: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("plex.tv rejected this token")
	default:
		return fmt.Errorf("plex.tv answered %d when checking the token", resp.StatusCode)
	}
}
