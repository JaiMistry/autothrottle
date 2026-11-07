package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
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

// SetQBittorrentLimits sets qBittorrent's ordinary global speed limits:
// global, not per-torrent, since the point is to free the link whatever
// is queued. Takes KB/s; 0 means unlimited. Written through the
// preferences by name, since the transfer API's setters write whichever
// set is in force, alternative speed's included.
func SetQBittorrentLimits(ctx context.Context, baseURL, username, password string, downloadLimitKBps, uploadLimitKBps int64) error {
	return QBittorrent{}.Apply(ctx, baseURL, username, password, Desired{
		Normal: Limits{DownloadKBps: downloadLimitKBps, UploadKBps: uploadLimitKBps},
	})
}

// Inspect reads qBittorrent's limits, alternative speed and scheduler.
func (QBittorrent) Inspect(ctx context.Context, baseURL, username, password string) (ClientState, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	client, err := qbittorrentClient(ctx, baseURL, username, password)
	if err != nil {
		return ClientState{}, err
	}
	var prefs struct {
		DownloadLimit    *int64 `json:"dl_limit"`
		UploadLimit      *int64 `json:"up_limit"`
		AltDownloadLimit *int64 `json:"alt_dl_limit"`
		AltUploadLimit   *int64 `json:"alt_up_limit"`
		SchedulerEnabled *bool  `json:"scheduler_enabled"`
	}
	if err := qbittorrentGetJSON(ctx, client, baseURL, "/api/v2/app/preferences", &prefs); err != nil {
		return ClientState{}, fmt.Errorf("reading preferences: %w", err)
	}
	if prefs.DownloadLimit == nil || prefs.UploadLimit == nil || prefs.AltDownloadLimit == nil || prefs.AltUploadLimit == nil || prefs.SchedulerEnabled == nil {
		return ClientState{}, fmt.Errorf("reading preferences: qBittorrent didn't report its speed limits")
	}
	on, err := qbittorrentAlternativeOn(ctx, client, baseURL)
	if err != nil {
		return ClientState{}, err
	}
	state := ClientState{
		Normal:        Limits{DownloadKBps: *prefs.DownloadLimit / 1024, UploadKBps: *prefs.UploadLimit / 1024},
		Alternative:   Limits{DownloadKBps: *prefs.AltDownloadLimit / 1024, UploadKBps: *prefs.AltUploadLimit / 1024},
		AlternativeOn: on,
	}
	if *prefs.SchedulerEnabled {
		state.Scheduler = "alternative speed schedule"
	}
	return state, nil
}

// Apply writes the limits by name, then switches alternative speed.
func (QBittorrent) Apply(ctx context.Context, baseURL, username, password string, want Desired) error {
	baseURL = strings.TrimRight(baseURL, "/")
	client, err := qbittorrentClient(ctx, baseURL, username, password)
	if err != nil {
		return err
	}
	prefs := map[string]int64{
		"dl_limit": qbittorrentBytes(want.Normal.DownloadKBps),
		"up_limit": qbittorrentBytes(want.Normal.UploadKBps),
	}
	if want.Alternative != nil {
		prefs["alt_dl_limit"] = qbittorrentBytes(want.Alternative.DownloadKBps)
		prefs["alt_up_limit"] = qbittorrentBytes(want.Alternative.UploadKBps)
	}
	if err := qbittorrentSetPreferences(ctx, client, baseURL, prefs); err != nil {
		return fmt.Errorf("setting speed limits: %w", err)
	}
	if want.AlternativeOn != nil {
		if err := qbittorrentSetAlternative(ctx, client, baseURL, *want.AlternativeOn); err != nil {
			return fmt.Errorf("switching alternative speed: %w", err)
		}
	}
	return nil
}

// ClearConflicts turns the scheduler off, keeping its times, then
// alternative speed, which the scheduler may have left on.
func (QBittorrent) ClearConflicts(ctx context.Context, baseURL, username, password string) error {
	baseURL = strings.TrimRight(baseURL, "/")
	client, err := qbittorrentClient(ctx, baseURL, username, password)
	if err != nil {
		return err
	}
	if err := qbittorrentSetPreferences(ctx, client, baseURL, map[string]bool{"scheduler_enabled": false}); err != nil {
		return fmt.Errorf("turning off the alternative speed schedule: %w", err)
	}
	if err := qbittorrentSetAlternative(ctx, client, baseURL, false); err != nil {
		return fmt.Errorf("switching off alternative speed: %w", err)
	}
	return nil
}

// qbittorrentMaxKBps is the most qBittorrent can hold: it reads limits as
// a 32-bit count of bytes, and a larger one would become unlimited.
const qbittorrentMaxKBps = (1<<31 - 1) / 1024

// qbittorrentBytes converts KB/s to the API's bytes/s, 0 staying
// unlimited.
func qbittorrentBytes(kbps int64) int64 {
	return min(max(kbps, 0), qbittorrentMaxKBps) * 1024
}

func qbittorrentAlternativeOn(ctx context.Context, client *http.Client, baseURL string) (bool, error) {
	var mode int
	if err := qbittorrentGetJSON(ctx, client, baseURL, "/api/v2/transfer/speedLimitsMode", &mode); err != nil {
		return false, fmt.Errorf("reading alternative speed: %w", err)
	}
	return mode == 1, nil
}

// qbittorrentSetAlternative switches alternative speed on or off. 4.5
// and later can be told which; before that it can only be toggled.
func qbittorrentSetAlternative(ctx context.Context, client *http.Client, baseURL string, on bool) error {
	mode := "0"
	if on {
		mode = "1"
	}
	status, err := qbittorrentPost(ctx, client, baseURL, "/api/v2/transfer/setSpeedLimitsMode", url.Values{"mode": {mode}})
	if err != nil || status == http.StatusOK {
		return err
	}
	if status != http.StatusNotFound {
		return fmt.Errorf("server responded with unexpected status %d", status)
	}
	current, err := qbittorrentAlternativeOn(ctx, client, baseURL)
	if err != nil || current == on {
		return err
	}
	if status, err = qbittorrentPost(ctx, client, baseURL, "/api/v2/transfer/toggleSpeedLimitsMode", nil); err == nil && status != http.StatusOK {
		err = fmt.Errorf("server responded with unexpected status %d", status)
	}
	return err
}

func qbittorrentSetPreferences(ctx context.Context, client *http.Client, baseURL string, prefs any) error {
	encoded, err := json.Marshal(prefs)
	if err != nil {
		return fmt.Errorf("encoding preferences: %w", err)
	}
	status, err := qbittorrentPost(ctx, client, baseURL, "/api/v2/app/setPreferences", url.Values{"json": {string(encoded)}})
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("server responded with unexpected status %d", status)
	}
	return err
}

// qbittorrentPost sends a form and returns the status.
func qbittorrentPost(ctx context.Context, client *http.Client, baseURL, path string, form url.Values) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", baseURL)
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("connecting to %s: %w", baseURL, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

func qbittorrentGetJSON(ctx context.Context, client *http.Client, baseURL, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Referer", baseURL)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server responded with unexpected status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
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
