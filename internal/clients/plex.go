// Package clients talks to the media servers and download clients:
// connection tests, live session watchers, and rate control. Each
// protocol's quirks stay in here, behind the interfaces in
// interfaces.go.
package clients

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// RequestTimeout bounds each request a connection test makes.
const RequestTimeout = 8 * time.Second

// TestPlex checks a Plex server answers with the token, and that the
// token is good. A server that refuses requests without a token has just
// checked it itself. One that lets this address in without a token (Plex
// can, on its own network) accepts any token at all, so plex.tv checks
// it instead; if plex.tv can't be reached, as on a network without
// internet, the test passes with a Caveat, since the server lets
// AutoThrottle in regardless.
func TestPlex(ctx context.Context, baseURL, token string) error {
	baseURL = strings.TrimRight(baseURL, "/")
	status, err := plexRootStatus(ctx, baseURL, token)
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

	open, err := plexRootStatus(ctx, baseURL, "")
	if err != nil {
		return err
	}
	if open == http.StatusUnauthorized {
		return nil
	}

	err = verifyPlexToken(ctx, token)
	if errors.Is(err, errPlexTVUnreachable) {
		return &Caveat{Note: "Plex lets AutoThrottle in from here without a token, so only plex.tv can check this one, and plex.tv couldn't be reached. Throttling works regardless."}
	}
	return err
}

// plexRootStatus asks the server's root, with the token or, if it is
// empty, without one.
func plexRootStatus(ctx context.Context, baseURL, token string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/", nil)
	if err != nil {
		return 0, fmt.Errorf("building request: %w", err)
	}
	if token != "" {
		req.Header.Set("X-Plex-Token", token)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := newHTTPClient(RequestTimeout).Do(req)
	if err != nil {
		return 0, fmt.Errorf("connecting to %s: %w", baseURL, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// Caveat is a connection test that passed, with something to know about
// the result.
type Caveat struct{ Note string }

func (c *Caveat) Error() string { return c.Note }

var errPlexTVUnreachable = errors.New("plex.tv couldn't be reached")

// plexAccountURL is where a token is actually checked. A variable so
// tests can stand in for plex.tv.
var plexAccountURL = "https://plex.tv/api/v2/user"

// verifyPlexToken checks the token with plex.tv, which answers 401 for a
// bad one, for a server that would let any token in.
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
		return fmt.Errorf("%w: %w", errPlexTVUnreachable, err)
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
