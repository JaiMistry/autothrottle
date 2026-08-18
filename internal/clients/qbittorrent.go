package clients

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// TestQBittorrent checks that baseURL is a reachable qBittorrent WebUI.
// If username/password are set, it exercises the real login endpoint
// (the WebUI API returns HTTP 200 with body "Fails." on bad
// credentials, not a 401/403 — checking the body is required, status
// code alone isn't enough). With no credentials (qBittorrent configured
// to bypass auth for whitelisted/local clients), it falls back to the
// one endpoint qBittorrent's API documents as unauthenticated.
func TestQBittorrent(ctx context.Context, baseURL, username, password string) error {
	baseURL = strings.TrimRight(baseURL, "/")
	client := &http.Client{Timeout: testTimeout}

	if username == "" && password == "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v2/app/webapiVersion", nil)
		if err != nil {
			return fmt.Errorf("building request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("connecting to %s: %w", baseURL, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("server responded with unexpected status %d", resp.StatusCode)
		}
		return nil
	}

	form := url.Values{"username": {username}, "password": {password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", baseURL)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server responded with unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}
	if strings.TrimSpace(string(body)) != "Ok." {
		return fmt.Errorf("login rejected: username or password is incorrect")
	}
	return nil
}
