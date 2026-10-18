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

// TestQBittorrent checks that baseURL is a reachable qBittorrent WebUI,
// logging in when credentials are set. Without them (auth bypassed for
// local clients) it uses the one endpoint that needs no login.
func TestQBittorrent(ctx context.Context, baseURL, username, password string) error {
	baseURL = strings.TrimRight(baseURL, "/")

	if username == "" && password == "" {
		client := newHTTPClient(RequestTimeout)
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

// SetQBittorrentLimits sets qBittorrent's global speed limits: global,
// not per-torrent, since the point is to free the link whatever is
// queued. Takes KB/s; the API wants bytes/s. 0 means unlimited.
func SetQBittorrentLimits(ctx context.Context, baseURL, username, password string, downloadLimitKBps, uploadLimitKBps int64) error {
	baseURL = strings.TrimRight(baseURL, "/")

	client, err := qbittorrentClient(ctx, baseURL, username, password)
	if err != nil {
		return err
	}

	if err := qbittorrentSetLimit(ctx, client, baseURL, "/api/v2/transfer/setDownloadLimit", downloadLimitKBps); err != nil {
		return fmt.Errorf("setting download limit: %w", err)
	}
	if err := qbittorrentSetLimit(ctx, client, baseURL, "/api/v2/transfer/setUploadLimit", uploadLimitKBps); err != nil {
		return fmt.Errorf("setting upload limit: %w", err)
	}
	return nil
}

// qbittorrentClient returns a client for the WebUI API, logged in when
// credentials are set.
func qbittorrentClient(ctx context.Context, baseURL, username, password string) (*http.Client, error) {
	if username == "" && password == "" {
		return newHTTPClient(RequestTimeout), nil
	}
	return qbittorrentLogin(ctx, baseURL, username, password)
}

// qbittorrentLogin returns a client carrying the session cookie.
func qbittorrentLogin(ctx context.Context, baseURL, username, password string) (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("creating cookie jar: %w", err)
	}
	client := newHTTPClient(RequestTimeout)
	client.Jar = jar

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

	// 4.x answers 200 "Ok." or 200 "Fails."; 5.x answers 204 with no body,
	// still setting the cookie. Both shapes must be accepted.
	switch {
	case resp.StatusCode == http.StatusNoContent:
		return client, nil
	case resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("login refused: too many failed attempts, this address is banned for a while")
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("server responded with unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	switch strings.TrimSpace(string(body)) {
	case "Ok.", "":
		return client, nil
	default:
		return nil, fmt.Errorf("login rejected: username or password is incorrect")
	}
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
