package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Plex's sign-in flow, so a user can authorise AutoThrottle instead of
// hunting an X-Plex-Token out of a URL in a support article.
//
// It is a three-step dance: ask plex.tv for a PIN, send the user to an
// authorisation page carrying that PIN's code, then poll the PIN until
// Plex attaches a token to it. Nothing is stored until the token
// arrives, and the token that arrives is the same kind of credential a
// pasted X-Plex-Token is — so the rest of the app is unchanged.
const (
	plexPinsURL = "https://plex.tv/api/v2/pins"
	plexAuthURL = "https://app.plex.tv/auth"

	// plexProduct identifies this app in the user's Plex account under
	// authorised devices, so they can see and revoke it.
	plexProduct = "AutoThrottle"
)

// PlexPin is an in-flight sign-in attempt.
type PlexPin struct {
	ID       int    `json:"id"`
	Code     string `json:"code"`
	ClientID string `json:"-"`
	// AuthURL is where the user completes the sign-in.
	AuthURL string `json:"-"`
}

// RequestPlexPin starts a sign-in. clientID must be stable for the
// lifetime of the attempt — Plex ties the PIN to it, and polling with a
// different one returns nothing.
func RequestPlexPin(ctx context.Context, clientID string) (PlexPin, error) {
	form := url.Values{"strong": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, plexPinsURL, strings.NewReader(form.Encode()))
	if err != nil {
		return PlexPin{}, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	plexAuthHeaders(req, clientID)

	resp, err := (&http.Client{Timeout: RequestTimeout}).Do(req)
	if err != nil {
		return PlexPin{}, fmt.Errorf("contacting plex.tv: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return PlexPin{}, fmt.Errorf("plex.tv answered %d", resp.StatusCode)
	}

	var pin PlexPin
	if err := json.NewDecoder(resp.Body).Decode(&pin); err != nil {
		return PlexPin{}, fmt.Errorf("reading pin: %w", err)
	}
	pin.ClientID = clientID
	pin.AuthURL = plexAuthPageURL(clientID, pin.Code)
	return pin, nil
}

// PollPlexPin checks whether the user has finished signing in. An empty
// token with a nil error means "not yet" — the caller should ask again
// rather than treating it as a failure.
func PollPlexPin(ctx context.Context, clientID string, pinID int) (string, error) {
	endpoint := fmt.Sprintf("%s/%d", plexPinsURL, pinID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}
	plexAuthHeaders(req, clientID)

	resp, err := (&http.Client{Timeout: RequestTimeout}).Do(req)
	if err != nil {
		return "", fmt.Errorf("contacting plex.tv: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		// Plex expires a PIN after a few minutes; a new one is needed.
		return "", fmt.Errorf("this sign-in expired; start again")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("plex.tv answered %d", resp.StatusCode)
	}

	var out struct {
		AuthToken string `json:"authToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("reading pin: %w", err)
	}
	return out.AuthToken, nil
}

// plexAuthPageURL is where the user is sent to approve the request.
func plexAuthPageURL(clientID, code string) string {
	q := url.Values{}
	q.Set("clientID", clientID)
	q.Set("code", code)
	q.Set("context[device][product]", plexProduct)
	return plexAuthURL + "#?" + q.Encode()
}

// plexAuthHeaders identifies this app to plex.tv. Plex requires the
// client identifier on every call in the flow and matches it against
// the one the PIN was created with.
func plexAuthHeaders(req *http.Request, clientID string) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Product", plexProduct)
	req.Header.Set("X-Plex-Version", "1.0")
	req.Header.Set("X-Plex-Client-Identifier", clientID)
	req.Header.Set("X-Plex-Device", plexProduct)
	req.Header.Set("X-Plex-Device-Name", plexProduct)
	req.Header.Set("X-Plex-Platform", "Docker")
}
