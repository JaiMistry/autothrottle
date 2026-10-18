// Package clients talks to the media servers and download clients:
// connection tests, live session watchers, and rate control. Each
// protocol's quirks stay in here, behind the interfaces in
// interfaces.go.
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

	client := newHTTPClient(RequestTimeout)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		// Reachable is all this proves; see verifyPlexToken.
	case http.StatusUnauthorized:
		return fmt.Errorf("server reachable but token was rejected (401)")
	default:
		return fmt.Errorf("server responded with unexpected status %d", resp.StatusCode)
	}

	return verifyPlexToken(ctx, token)
}

// plexAccountURL is where a token is actually checked. A variable so
// tests can stand in for plex.tv.
var plexAccountURL = "https://plex.tv/api/v2/user"

// verifyPlexToken checks the token with plex.tv. A Plex server does not
// enforce tokens on the LAN by default (a wrong or missing token still
// gets 200), so testing the server alone passes tokens that will never
// work. plex.tv answers 401 for a bad token.
func verifyPlexToken(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, plexAccountURL, nil)
	if err != nil {
		return fmt.Errorf("building token check: %w", err)
	}
	// plex.tv requires a client identifier; any will do for a read.
	plexAuthHeaders(req, plexProduct)
	req.Header.Set("X-Plex-Token", token)

	resp, err := newHTTPClient(RequestTimeout).Do(req)
	if err != nil {
		// A failure, not a pass: nothing vouched for the token.
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
