package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
)

// transmissionRPCPath is the one endpoint; the method is in the body.
const transmissionRPCPath = "/transmission/rpc"

// transmissionSessionHeader carries Transmission's CSRF token. The first
// request is expected to fail with 409 and this header, which every
// later request must echo; see transmissionCall.
const transmissionSessionHeader = "X-Transmission-Session-Id"

type transmissionRequest struct {
	Method    string `json:"method"`
	Arguments any    `json:"arguments,omitempty"`
}

type transmissionResponse struct {
	// Result is "success" or a failure message: Transmission reports
	// failures here with a 200 status.
	Result    string          `json:"result"`
	Arguments json.RawMessage `json:"arguments"`
}

// TestTransmission checks that baseURL reaches a Transmission daemon
// and that the credentials, if any, are accepted.
func TestTransmission(ctx context.Context, baseURL, username, password string) error {
	_, err := transmissionCall(ctx, baseURL, username, password, "session-get", nil)
	return err
}

// SetTransmissionLimits sets the ordinary global limits. 0 disables a
// limit, since a literal 0 would stop all traffic.
func SetTransmissionLimits(ctx context.Context, baseURL, username, password string, downloadLimitKBps, uploadLimitKBps int64) error {
	return Transmission{}.Apply(ctx, baseURL, username, password, Desired{
		Normal: Limits{DownloadKBps: downloadLimitKBps, UploadKBps: uploadLimitKBps},
	})
}

// transmissionSession is the part of session-get AutoThrottle reads.
// Numbers are floats: 4.1.0 sent the speed limits with a decimal point.
type transmissionSession struct {
	Down           *float64 `json:"speed-limit-down"`
	DownEnabled    *bool    `json:"speed-limit-down-enabled"`
	Up             *float64 `json:"speed-limit-up"`
	UpEnabled      *bool    `json:"speed-limit-up-enabled"`
	AltDown        *float64 `json:"alt-speed-down"`
	AltUp          *float64 `json:"alt-speed-up"`
	AltEnabled     *bool    `json:"alt-speed-enabled"`
	AltTimeEnabled *bool    `json:"alt-speed-time-enabled"`
}

// Inspect reads Transmission's limits, alternative speed and schedule.
func (Transmission) Inspect(ctx context.Context, baseURL, username, password string) (ClientState, error) {
	args := map[string]any{"fields": []string{
		"speed-limit-down", "speed-limit-down-enabled", "speed-limit-up", "speed-limit-up-enabled",
		"alt-speed-down", "alt-speed-up", "alt-speed-enabled", "alt-speed-time-enabled",
	}}
	raw, err := transmissionCall(ctx, baseURL, username, password, "session-get", args)
	if err != nil {
		return ClientState{}, err
	}
	var got transmissionSession
	if err := json.Unmarshal(raw, &got); err != nil {
		return ClientState{}, fmt.Errorf("decoding session: %w", err)
	}
	if got.Down == nil || got.DownEnabled == nil || got.Up == nil || got.UpEnabled == nil ||
		got.AltDown == nil || got.AltUp == nil || got.AltEnabled == nil || got.AltTimeEnabled == nil {
		return ClientState{}, fmt.Errorf("decoding session: Transmission didn't report its speed limits")
	}
	limit := func(value float64, enabled bool) int64 {
		if !enabled {
			return 0
		}
		return int64(math.Round(value))
	}
	state := ClientState{
		Normal:        Limits{DownloadKBps: limit(*got.Down, *got.DownEnabled), UploadKBps: limit(*got.Up, *got.UpEnabled)},
		Alternative:   Limits{DownloadKBps: limit(*got.AltDown, true), UploadKBps: limit(*got.AltUp, true)},
		AlternativeOn: *got.AltEnabled,
	}
	if *got.AltTimeEnabled {
		state.Scheduler = "alternative speed schedule"
	}
	return state, nil
}

// Apply sets the limits and alternative speed in one call. Alternative
// speed has no on/off per direction: 0 there is unlimited.
func (Transmission) Apply(ctx context.Context, baseURL, username, password string, want Desired) error {
	args := map[string]any{
		"speed-limit-down":         want.Normal.DownloadKBps,
		"speed-limit-down-enabled": want.Normal.DownloadKBps > 0,
		"speed-limit-up":           want.Normal.UploadKBps,
		"speed-limit-up-enabled":   want.Normal.UploadKBps > 0,
	}
	if want.Alternative != nil {
		args["alt-speed-down"] = want.Alternative.DownloadKBps
		args["alt-speed-up"] = want.Alternative.UploadKBps
	}
	if want.AlternativeOn != nil {
		args["alt-speed-enabled"] = *want.AlternativeOn
	}
	if _, err := transmissionCall(ctx, baseURL, username, password, "session-set", args); err != nil {
		return fmt.Errorf("setting speed limits: %w", err)
	}
	return nil
}

// ClearConflicts turns off the alternative speed schedule, keeping its
// times, and alternative speed.
func (Transmission) ClearConflicts(ctx context.Context, baseURL, username, password string) error {
	args := map[string]any{"alt-speed-time-enabled": false, "alt-speed-enabled": false}
	if _, err := transmissionCall(ctx, baseURL, username, password, "session-set", args); err != nil {
		return fmt.Errorf("turning off the alternative speed schedule: %w", err)
	}
	return nil
}

// transmissionCall performs one RPC, completing the session-id
// handshake if asked. One retry at most: a second 409 means the server
// is not behaving like Transmission.
func transmissionCall(ctx context.Context, baseURL, username, password, method string, args any) (json.RawMessage, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	rpcURL := baseURL + transmissionRPCPath

	body, err := json.Marshal(transmissionRequest{Method: method, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("encoding request: %w", err)
	}

	client := newHTTPClient(RequestTimeout)
	sessionID := ""

	for attempt := 0; attempt < 2; attempt++ {
		raw, newSessionID, err := transmissionAttempt(ctx, client, rpcURL, username, password, sessionID, body)
		if err != nil {
			return nil, err
		}
		if newSessionID == "" {
			return raw, nil
		}
		sessionID = newSessionID
	}
	return nil, fmt.Errorf("server kept asking for a new session id; is %s a Transmission RPC endpoint?", rpcURL)
}

// transmissionAttempt makes one request. A non-empty returned session
// id means "retry with this"; an empty one with a nil error means the
// call succeeded and raw holds the response arguments.
func transmissionAttempt(ctx context.Context, client *http.Client, rpcURL, username, password, sessionID string, body []byte) (json.RawMessage, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set(transmissionSessionHeader, sessionID)
	}
	// Unauthenticated RPC must get no Basic header at all.
	if username != "" || password != "" {
		req.SetBasicAuth(username, password)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("connecting to %s: %w", rpcURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusConflict:
		next := resp.Header.Get(transmissionSessionHeader)
		if next == "" {
			return nil, "", fmt.Errorf("server sent 409 without a %s header", transmissionSessionHeader)
		}
		return nil, next, nil
	case http.StatusUnauthorized:
		return nil, "", fmt.Errorf("server reachable but credentials were rejected (401)")
	case http.StatusNotFound:
		return nil, "", fmt.Errorf("no RPC endpoint at %s; check the URL is the daemon's root", rpcURL)
	default:
		return nil, "", fmt.Errorf("server responded with unexpected status %d", resp.StatusCode)
	}

	var decoded transmissionResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, "", fmt.Errorf("decoding response: %w", err)
	}
	if decoded.Result != "success" {
		return nil, "", fmt.Errorf("request rejected: %s", decoded.Result)
	}
	return decoded.Arguments, "", nil
}
