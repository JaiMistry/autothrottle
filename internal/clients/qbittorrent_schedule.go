package clients

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

var qbRateFields = [...]string{"dl_limit", "up_limit", "alt_dl_limit", "alt_up_limit"}

type qbScheduleState struct {
	Identity string           `json:"identity"`
	Original map[string]int64 `json:"original"`
	Applied  map[string]int64 `json:"applied"`
}

// ApplyScheduledLimits caps BOTH rate banks while playback is active. The
// qBittorrent scheduler can therefore change modes normally, including a
// window crossing midnight. Only the four speed fields are ever written:
// scheduling, days, times and the selected mode remain qBittorrent's own.
func (QBittorrent) ApplyScheduledLimits(ctx context.Context, baseURL, username, password string, throttled bool, down, up int64, store LimitStateStore, key string) (*AppliedLimits, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	raw, _, err := store.GetSetting(key)
	if err != nil {
		return nil, fmt.Errorf("reading scheduled rate recovery: %w", err)
	}
	var state qbScheduleState
	identity := sha256.Sum256([]byte(baseURL + "\x00" + username))
	id := hex.EncodeToString(identity[:])
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &state); err != nil || state.Identity != id {
			return nil, fmt.Errorf("scheduled rate recovery does not match this downloader")
		}
		for _, field := range qbRateFields {
			original, ok := state.Original[field]
			applied, had := state.Applied[field]
			if !ok || !had || original < 0 || applied < 0 {
				return nil, fmt.Errorf("invalid scheduled rate recovery")
			}
		}
	}
	client, err := qbittorrentClient(ctx, baseURL, username, password)
	if err != nil {
		return nil, err
	}
	var prefs map[string]json.RawMessage
	if err := qbReadJSON(ctx, client, baseURL, "/api/v2/app/preferences", &prefs); err != nil {
		return nil, err
	}
	var scheduled bool
	if value, ok := prefs["scheduler_enabled"]; !ok || json.Unmarshal(value, &scheduled) != nil || string(value) == "null" {
		return nil, fmt.Errorf("qBittorrent did not report its scheduler state")
	}
	if !scheduled && raw == "" {
		return nil, nil
	}
	current := make(map[string]int64, 4)
	for _, field := range qbRateFields {
		var value int64
		b, ok := prefs[field]
		if !ok || json.Unmarshal(b, &value) != nil || string(b) == "null" || value < 0 {
			return nil, fmt.Errorf("qBittorrent did not report a valid %s", field)
		}
		current[field] = value
	}
	var mode int
	if err := qbReadJSON(ctx, client, baseURL, "/api/v2/transfer/speedLimitsMode", &mode); err != nil {
		return nil, err
	}
	if mode != 0 && mode != 1 {
		return nil, fmt.Errorf("qBittorrent reported an invalid speed mode")
	}
	changes := make(map[string]int64)
	if throttled {
		if down < 0 || up < 0 || down > (1<<31-1)/1024 || up > (1<<31-1)/1024 {
			return nil, fmt.Errorf("scheduled rate limits are outside qBittorrent's supported range")
		}
		if raw == "" {
			state = qbScheduleState{Identity: id, Original: make(map[string]int64, 4), Applied: make(map[string]int64, 4)}
		}
		for i, field := range qbRateFields {
			// Respect rate edits made outside AutoThrottle while playing.
			if raw == "" || current[field] != state.Applied[field] {
				state.Original[field] = current[field]
			}
			capBytes := down * 1024
			if i%2 == 1 {
				capBytes = up * 1024
			}
			limit := state.Original[field]
			if capBytes > 0 && (limit == 0 || capBytes < limit) {
				limit = capBytes
			}
			state.Applied[field] = limit
			if current[field] != limit {
				changes[field] = limit
			}
		}
		encoded, err := json.Marshal(state)
		if err != nil {
			return nil, err
		}
		// Persist BEFORE any remote write, including failed/partial writes.
		if string(encoded) != raw {
			if err := store.SetSetting(key, string(encoded)); err != nil {
				return nil, fmt.Errorf("saving scheduled rate recovery: %w", err)
			}
		}
	} else if raw != "" {
		for _, field := range qbRateFields {
			// Never undo an external edit that replaced our last limit.
			if current[field] == state.Applied[field] && current[field] != state.Original[field] {
				changes[field] = state.Original[field]
			}
		}
	}
	if len(changes) > 0 {
		encoded, err := json.Marshal(changes)
		if err != nil {
			return nil, err
		}
		form := url.Values{"json": {string(encoded)}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v2/app/setPreferences", strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Referer", baseURL)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("setting scheduled rate limits: status %d", resp.StatusCode)
		}
		for field, value := range changes {
			current[field] = value
		}
	}
	if !throttled && raw != "" {
		if err := store.SetSetting(key, ""); err != nil {
			return nil, fmt.Errorf("clearing scheduled rate recovery: %w", err)
		}
	}
	if mode == 1 {
		return &AppliedLimits{DownloadKBps: current["alt_dl_limit"] / 1024, UploadKBps: current["alt_up_limit"] / 1024, FollowSchedule: scheduled}, nil
	}
	return &AppliedLimits{DownloadKBps: current["dl_limit"] / 1024, UploadKBps: current["up_limit"] / 1024, FollowSchedule: scheduled}, nil
}

func qbReadJSON(ctx context.Context, client *http.Client, baseURL, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("building qBittorrent request: %w", err)
	}
	req.Header.Set("Referer", baseURL)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("reading qBittorrent preferences: status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
