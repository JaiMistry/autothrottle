package clients

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
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

	if username == "" && password == "" {
		client := &http.Client{Timeout: testTimeout}
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

	_, err := qbittorrentLogin(ctx, baseURL, username, password)
	return err
}

// SetQBittorrentLimits authenticates (when credentials are set) and
// sets qBittorrent's global download/upload speed limits. Deliberately
// global limits, not per-torrent — matches plan.md's scope of
// throttling the whole client, not individual torrents. Limits are
// KB/s per the app's schema convention; qBittorrent's API takes
// bytes/sec. 0 means unlimited on both sides.
func SetQBittorrentLimits(ctx context.Context, baseURL, username, password string, downloadLimitKBps, uploadLimitKBps int64) error {
	baseURL = strings.TrimRight(baseURL, "/")

	var client *http.Client
	if username != "" || password != "" {
		authed, err := qbittorrentLogin(ctx, baseURL, username, password)
		if err != nil {
			return err
		}
		client = authed
	} else {
		client = &http.Client{Timeout: testTimeout}
	}

	if err := qbittorrentSetLimit(ctx, client, baseURL, "/api/v2/transfer/setDownloadLimit", downloadLimitKBps); err != nil {
		return fmt.Errorf("setting download limit: %w", err)
	}
	if err := qbittorrentSetLimit(ctx, client, baseURL, "/api/v2/transfer/setUploadLimit", uploadLimitKBps); err != nil {
		return fmt.Errorf("setting upload limit: %w", err)
	}
	return nil
}

// qbittorrentLogin authenticates and returns an *http.Client carrying
// the resulting session cookie (via cookiejar) for subsequent calls.
func qbittorrentLogin(ctx context.Context, baseURL, username, password string) (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("creating cookie jar: %w", err)
	}
	client := &http.Client{Timeout: testTimeout, Jar: jar}

	form := url.Values{"username": {username}, "password": {password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", baseURL)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server responded with unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if strings.TrimSpace(string(body)) != "Ok." {
		return nil, fmt.Errorf("login rejected: username or password is incorrect")
	}
	return client, nil
}

func qbittorrentSetLimit(ctx context.Context, client *http.Client, baseURL, path string, limitKBps int64) error {
	limitBytesPerSec := limitKBps * 1024
	form := url.Values{"limit": {strconv.FormatInt(limitBytesPerSec, 10)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, strings.NewReader(form.Encode()))
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
	return nil
}
