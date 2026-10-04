package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Transmission's RPC lives at one endpoint and dispatches on a method
// name in the request body, rather than qBittorrent's one-path-per-
// operation REST layout.
const transmissionRPCPath = "/transmission/rpc"

// transmissionSessionHeader carries Transmission's CSRF token.
//
// The handshake is unusual enough to be worth stating plainly: the
// first request of a connection is *expected* to fail. Transmission
// answers it with 409 and puts a session id in this header, which the
// client must echo on every subsequent request. So a 409 here is not an
// error, it is step one of the protocol — see transmissionCall.
const transmissionSessionHeader = "X-Transmission-Session-Id"

type transmissionRequest struct {
	Method    string `json:"method"`
	Arguments any    `json:"arguments,omitempty"`
}

type transmissionResponse struct {
	// Result is "success" or a human-readable failure string.
	// Transmission reports application-level failures in here with a
	// 200 status, so the status code alone never confirms anything —
	// the same trap qBittorrent's login endpoint sets with "Fails."
	Result    string          `json:"result"`
	Arguments json.RawMessage `json:"arguments"`
}

// TestTransmission checks that baseURL reaches a Transmission daemon
// and that the credentials, if any, are accepted. session-get is the
// cheapest authenticated method that proves all three.
func TestTransmission(ctx context.Context, baseURL, username, password string) error {
	_, err := transmissionCall(ctx, baseURL, username, password, "session-get", nil)
	return err
}

// SetTransmissionLimits sets Transmission's global speed limits.
//
// The ordinary limits, not turtle mode, which is the user's own toggle
// and would be fought over. Limits are KB/s, which the RPC takes as is.
// 0 means unlimited, so it disables the limit: a literal 0 would stop
// all traffic.
func SetTransmissionLimits(ctx context.Context, baseURL, username, password string, downloadLimitKBps, uploadLimitKBps int64) error {
	args := map[string]any{
		"speed-limit-down":         downloadLimitKBps,
		"speed-limit-down-enabled": downloadLimitKBps > 0,
		"speed-limit-up":           uploadLimitKBps,
		"speed-limit-up-enabled":   uploadLimitKBps > 0,
	}
	if _, err := transmissionCall(ctx, baseURL, username, password, "session-set", args); err != nil {
		return fmt.Errorf("setting speed limits: %w", err)
	}
	return nil
}

// TransmissionRates reads current global transfer rates in bytes per
// second, for the bandwidth sampler.
func TransmissionRates(ctx context.Context, baseURL, username, password string) (int64, int64, error) {
	raw, err := transmissionCall(ctx, baseURL, username, password, "session-stats", nil)
	if err != nil {
		return 0, 0, err
	}

	var stats struct {
		DownloadSpeed int64 `json:"downloadSpeed"`
		UploadSpeed   int64 `json:"uploadSpeed"`
	}
	if err := json.Unmarshal(raw, &stats); err != nil {
		return 0, 0, fmt.Errorf("decoding session stats: %w", err)
	}
	return stats.DownloadSpeed, stats.UploadSpeed, nil
}

// transmissionCall performs one RPC, completing the session-id
// handshake if the daemon asks for it.
//
// At most one retry: the 409 either yields a usable session id or the
// server is not behaving like Transmission, and retrying past that
// would just loop against a broken endpoint.
func transmissionCall(ctx context.Context, baseURL, username, password, method string, args any) (json.RawMessage, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	rpcURL := baseURL + transmissionRPCPath

	body, err := json.Marshal(transmissionRequest{Method: method, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("encoding request: %w", err)
	}

	client := &http.Client{Timeout: RequestTimeout}
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
	// Transmission's RPC can be left unauthenticated, in which case
	// sending an empty Basic header would be worse than sending none.
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
