package clients

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// Emby and Jellyfin speak nearly the same protocol — Jellyfin forked
// from Emby 3.5.2 and kept the Sessions API and the notification
// WebSocket largely intact — so they share one implementation
// parameterised by the handful of things that actually differ.
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

// embyNowPlaying is what a session has loaded. Absent (null) unless
// something is actually playing, which is what distinguishes an idle
// connected client from a watching one.
type embyNowPlaying struct {
	Name         string `json:"Name"`
	SeriesName   string `json:"SeriesName"`
	SeasonNumber *int   `json:"ParentIndexNumber"`
	Episode      *int   `json:"IndexNumber"`
	// Ticks are 100-nanosecond units, which is what .NET's TimeSpan
	// serialises to — not milliseconds like Plex's.
	RunTimeTicks int64 `json:"RunTimeTicks"`
}

// embySession is the subset of /Sessions this app reads.
type embySession struct {
	ID string `json:"Id"`
	// RemoteEndPoint is the client's address, as "IP" or "IP:port".
	// This is what locality is derived from — see embySessionIsLocal.
	RemoteEndPoint string `json:"RemoteEndPoint"`
	// NowPlayingItem is absent (null) unless the session has media
	// loaded, which is what distinguishes an idle connected client
	// from one actually playing something.
	NowPlayingItem *embyNowPlaying `json:"NowPlayingItem"`
	DeviceName     string          `json:"DeviceName"`
	PlayState      struct {
		IsPaused      bool  `json:"IsPaused"`
		PositionTicks int64 `json:"PositionTicks"`
	} `json:"PlayState"`
}

// embyTick is the duration of one .NET tick.
const embyTick = 100 * time.Nanosecond

// position and duration report where playback has reached, or zero when
// the server did not say.
func (s embySession) position() time.Duration {
	return time.Duration(s.PlayState.PositionTicks) * embyTick
}

func (s embySession) duration() time.Duration {
	if s.NowPlayingItem == nil {
		return 0
	}
	return time.Duration(s.NowPlayingItem.RunTimeTicks) * embyTick
}

// label renders what is playing, matching the form Plex uses so a
// mixed setup reads consistently: a series with its numbering, or the
// item's own name for anything else.
func (s embySession) label() string {
	if s.NowPlayingItem == nil {
		return ""
	}
	item := s.NowPlayingItem
	if item.SeriesName != "" {
		if item.SeasonNumber != nil && item.Episode != nil {
			return fmt.Sprintf("%s S%dE%d", item.SeriesName, *item.SeasonNumber, *item.Episode)
		}
		return item.SeriesName
	}
	return item.Name
}

// embyStateCounts is the one definition of which sessions exist, shared
// by the socket and snapshot paths. A paused session has media loaded,
// so it counts; how long it goes on counting is pausedTracker's job.
func embyStateCounts(s embySession) bool {
	return s.NowPlayingItem != nil
}

// embySessionIsLocal reports whether a session's client is on the
// local network, for the local-playback exemption.
//
// Unlike Plex, these servers report only the client's address, so
// locality is inferred from it being loopback, private or link-local —
// which judges a VPN client local though its stream crosses the WAN.
// An unparseable address is judged non-local: throttling when unsure
// costs some speed, never throttling is a bug with no symptom.
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

	client := &http.Client{Timeout: RequestTimeout}
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
// WebSocket pushes, for the reconciliation poll.
func fetchEmbySessions(ctx context.Context, f embyFlavor, baseURL, apiKey string) ([]embySession, error) {
	// Unlike Plex's MediaContainer wrapper, these return a bare array.
	var sessions []embySession
	err := getJSON(ctx, baseURL+"/Sessions", func(r *http.Request) {
		f.setAuth(r, apiKey)
	}, &sessions)
	if err != nil {
		return nil, err
	}
	return sessions, nil
}
