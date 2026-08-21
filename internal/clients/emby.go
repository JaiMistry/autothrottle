package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Emby and Jellyfin speak nearly the same protocol — Jellyfin forked
// from Emby 3.5.2 and kept the Sessions API and the notification
// WebSocket largely intact — so they share one implementation
// parameterised by the handful of things that actually differ.
//
// NOTE: unlike the Plex client, none of this has been exercised
// against a live server yet (no Emby or Jellyfin instance on the
// homelab at time of writing). The endpoint shapes below come from
// both projects' API documentation and their own web clients' traffic.
// The unit tests pin the behaviour this code expects; what they cannot
// prove is that a real server sends exactly that. Treat the first live
// connection as the real verification.
type embyFlavor struct {
	name string
	// webSocketPath differs: Emby kept its original /embywebsocket,
	// Jellyfin renamed it to /socket.
	webSocketPath string
	// setAuth applies the flavour's accepted credential header. Both
	// take an API key created in the server's admin UI, not a
	// username/password.
	setAuth func(*http.Request, string)
}

var flavorEmby = embyFlavor{
	name:          "Emby",
	webSocketPath: "/embywebsocket",
	setAuth: func(r *http.Request, apiKey string) {
		r.Header.Set("X-Emby-Token", apiKey)
	},
}

var flavorJellyfin = embyFlavor{
	name:          "Jellyfin",
	webSocketPath: "/socket",
	setAuth: func(r *http.Request, apiKey string) {
		// Jellyfin's documented form. It still accepts the legacy
		// X-Emby-Token header, but that compatibility shim is exactly
		// the kind of thing a major version drops, so use the form
		// Jellyfin's own clients send.
		r.Header.Set("Authorization", fmt.Sprintf(
			`MediaBrowser Client="AutoThrottle", Device="AutoThrottle", DeviceId=%q, Version="1.0.0", Token=%q`,
			embyDeviceID, apiKey))
	},
}

// embyDeviceID identifies this app to the server. Emby requires a
// device ID on the WebSocket handshake; Jellyfin tolerates its absence
// but shows the connection more usefully with it. Fixed rather than
// random so reconnects reuse the same server-side session entry
// instead of littering the server's device list with one per restart.
const embyDeviceID = "autothrottle"

// embySession is the subset of /Sessions this app reads.
type embySession struct {
	ID string `json:"Id"`
	// RemoteEndPoint is the client's address, as "IP" or "IP:port".
	// This is what locality is derived from — see embySessionIsLocal.
	RemoteEndPoint string `json:"RemoteEndPoint"`
	// NowPlayingItem is absent (null) unless the session has media
	// loaded, which is what distinguishes an idle connected client
	// from one actually playing something.
	NowPlayingItem *struct {
		Name string `json:"Name"`
	} `json:"NowPlayingItem"`
	PlayState struct {
		IsPaused bool `json:"IsPaused"`
	} `json:"PlayState"`
}

// embyStateCounts mirrors plexStateCounts: the single definition of
// which sessions count toward throttling, shared by the WebSocket path
// and the snapshot path so the two can never disagree.
//
// Emby and Jellyfin have no equivalent of Plex's "buffering" state —
// a buffering client is still reported with NowPlayingItem set and
// IsPaused false, so it counts here for free.
func embyStateCounts(s embySession) bool {
	return s.NowPlayingItem != nil && !s.PlayState.IsPaused
}

// embySessionIsLocal reports whether a session's client is on the
// local network, for the local-playback exemption.
//
// This is the one place these servers are meaningfully weaker than
// Plex. Plex computes Player.local itself and hands it over; Emby and
// Jellyfin only report the client's address, so locality is inferred
// from the address being loopback, RFC1918/ULA private, or link-local.
//
// That inference carries the same caveat as Plex's flag, but more
// sharply: a client connected over a VPN into the same network gets a
// private address and is therefore judged local, while its stream
// genuinely crosses the WAN link. An unparseable or empty address is
// judged non-local, so it still counts toward throttling — the same
// fail-toward-throttling direction the Plex watcher takes when a
// locality lookup fails, and for the same reason: losing some download
// speed is a visible, recoverable cost, whereas silently never
// throttling is a bug with no symptom.
func embySessionIsLocal(remoteEndPoint string) bool {
	host := strings.TrimSpace(remoteEndPoint)
	if host == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	// A bare IPv6 address may still arrive bracketed when there was no
	// port for SplitHostPort to find.
	host = strings.Trim(host, "[]")

	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// TestEmby checks that baseURL reaches an Emby server and that apiKey
// is accepted.
func TestEmby(ctx context.Context, baseURL, apiKey string) error {
	return testEmbyFlavor(ctx, flavorEmby, baseURL, apiKey)
}

// TestJellyfin checks that baseURL reaches a Jellyfin server and that
// apiKey is accepted.
func TestJellyfin(ctx context.Context, baseURL, apiKey string) error {
	return testEmbyFlavor(ctx, flavorJellyfin, baseURL, apiKey)
}

// testEmbyFlavor hits /System/Info, which requires authentication.
//
// Deliberately not /System/Info/Public, which is unauthenticated and
// would 200 for any key — the same false-positive trap TestPlex
// documents for /identity. Unlike Plex, these servers do enforce the
// key on LAN requests, so this is a genuine credential check rather
// than a reachability check wearing one's clothes.
func testEmbyFlavor(ctx context.Context, f embyFlavor, baseURL, apiKey string) error {
	baseURL = strings.TrimRight(baseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/System/Info", nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	f.setAuth(req, apiKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: testTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("server reachable but API key was rejected (%d)", resp.StatusCode)
	case http.StatusNotFound:
		return fmt.Errorf("reached the server but it does not look like %s (404 for /System/Info)", f.name)
	default:
		return fmt.Errorf("server responded with unexpected status %d", resp.StatusCode)
	}
}

// fetchEmbySessions reads /Sessions, the REST equivalent of what the
// WebSocket pushes. Backs both the snapshot reconciliation poll and
// the connection test's session-shaped sibling.
func fetchEmbySessions(ctx context.Context, f embyFlavor, baseURL, apiKey string) ([]embySession, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/Sessions", nil)
	if err != nil {
		return nil, err
	}
	f.setAuth(req, apiKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: testTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	// Unlike Plex's MediaContainer wrapper, these return a bare array.
	var sessions []embySession
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		return nil, err
	}
	return sessions, nil
}
