package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"strings"
)

// Deluge is driven through its Web UI's JSON-RPC endpoint, the one its
// own web interface uses: the daemon's port speaks a binary protocol and
// wants a daemon account. The Web UI has a password and no username.
const delugeRPCPath = "/json"

type delugeRequest struct {
	Method string `json:"method"`
	Params []any  `json:"params"`
	ID     int    `json:"id"`
}

type delugeResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// delugeSession is a signed-in Web UI connected to a daemon.
type delugeSession struct {
	client *http.Client
	rpcURL string
	nextID int
}

// TestDeluge checks that baseURL reaches a Deluge Web UI, that the
// password is accepted, and that a daemon answers behind it.
func TestDeluge(ctx context.Context, baseURL, password string) error {
	s, err := delugeConnect(ctx, baseURL, password)
	if err != nil {
		return err
	}
	_, err = s.call(ctx, "core.get_config_value", "max_download_speed")
	return err
}

// SetDelugeLimits sets the daemon's global limits. Deluge counts in KiB/s,
// as AutoThrottle does, and -1 is unlimited: a literal 0 would stop all
// traffic.
func SetDelugeLimits(ctx context.Context, baseURL, password string, downloadLimitKBps, uploadLimitKBps int64) error {
	s, err := delugeConnect(ctx, baseURL, password)
	if err != nil {
		return err
	}
	limits := map[string]any{
		"max_download_speed": delugeRate(downloadLimitKBps),
		"max_upload_speed":   delugeRate(uploadLimitKBps),
	}
	if _, err := s.call(ctx, "core.set_config", limits); err != nil {
		return fmt.Errorf("setting speed limits: %w", err)
	}
	return nil
}

func delugeRate(kbps int64) int64 {
	if kbps <= 0 {
		return -1
	}
	return kbps
}

// delugeConnect signs in, and connects the Web UI to its daemon if it
// isn't already: a fresh Web UI starts disconnected, and until it
// connects every daemon call is an "Unknown method". The connection
// belongs to the Web UI, not the session, so this happens once.
func delugeConnect(ctx context.Context, baseURL, password string) (*delugeSession, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("creating cookie jar: %w", err)
	}
	client := newHTTPClient(RequestTimeout)
	client.Jar = jar
	s := &delugeSession{client: client, rpcURL: strings.TrimRight(baseURL, "/") + delugeRPCPath}

	var ok bool
	if err := s.decode(ctx, &ok, "auth.login", password); err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("server reachable but the Web UI password was rejected (unauthorized)")
	}

	var connected bool
	if err := s.decode(ctx, &connected, "web.connected"); err != nil {
		return nil, err
	}
	if connected {
		return s, nil
	}
	var hosts [][]any
	if err := s.decode(ctx, &hosts, "web.get_hosts"); err != nil {
		return nil, err
	}
	for _, host := range hosts {
		if len(host) == 0 {
			continue
		}
		id, _ := host[0].(string)
		if id == "" {
			continue
		}
		var status []any
		if err := s.decode(ctx, &status, "web.get_host_status", id); err != nil {
			return nil, err
		}
		if len(status) < 2 || status[1] != "Online" {
			continue
		}
		if _, err := s.call(ctx, "web.connect", id); err != nil {
			return nil, fmt.Errorf("connecting the Web UI to its daemon: %w", err)
		}
		return s, nil
	}
	return nil, fmt.Errorf("the Web UI is up, but no Deluge daemon it knows of is online")
}

// call makes one RPC and returns its result. Deluge reports failures in
// the body with a 200 status.
func (s *delugeSession) call(ctx context.Context, method string, params ...any) (json.RawMessage, error) {
	if params == nil {
		params = []any{}
	}
	s.nextID++
	body, err := json.Marshal(delugeRequest{Method: method, Params: params, ID: s.nextID})
	if err != nil {
		return nil, fmt.Errorf("encoding request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.rpcURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", s.rpcURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("no Deluge Web UI at %s; check the URL is the Web UI's root", s.rpcURL)
	default:
		return nil, fmt.Errorf("server responded with unexpected status %d", resp.StatusCode)
	}

	var decoded delugeResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decoding response: %w; is %s a Deluge Web UI?", err, s.rpcURL)
	}
	if decoded.Error != nil {
		return nil, fmt.Errorf("%s failed: %s", method, decoded.Error.Message)
	}
	return decoded.Result, nil
}

// decode makes one RPC and decodes its result into out.
func (s *delugeSession) decode(ctx context.Context, out any, method string, params ...any) error {
	raw, err := s.call(ctx, method, params...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decoding %s: %w", method, err)
	}
	return nil
}
