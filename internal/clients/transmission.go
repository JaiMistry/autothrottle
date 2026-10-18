package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// SetTransmissionLimits sets the ordinary global limits, not turtle
// mode (the user's own toggle). 0 disables a limit, since a literal 0
// would stop all traffic.
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
