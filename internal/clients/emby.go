package clients

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// Jellyfin forked Emby and kept its Sessions API and socket, so both
// share one implementation, parameterised by what differs.
type embyFlavor struct {
	name          string
	webSocketPath string
	setAuth       func(*http.Request, string)
	// keyInSocketURL is Emby's socket handshake, which takes the key as
	// api_key. Jellyfin 12 refuses that form and reads only the header.
	keyInSocketURL bool
}

var flavorEmby = embyFlavor{
	name:           "Emby",
	webSocketPath:  "/embywebsocket",
	keyInSocketURL: true,
	setAuth: func(r *http.Request, apiKey string) {
		r.Header.Set("X-Emby-Token", apiKey)
	},
}

var flavorJellyfin = embyFlavor{
	name:          "Jellyfin",
	webSocketPath: "/socket",
	setAuth: func(r *http.Request, apiKey string) {
		// Jellyfin's documented form; X-Emby-Token is a legacy shim.
		r.Header.Set("Authorization", fmt.Sprintf(
			`MediaBrowser Client="AutoThrottle", Device="AutoThrottle", DeviceId=%q, Version="1.0.0", Token=%q`,
			embyDeviceID, apiKey))
	},
}

// embyDeviceID is required by Emby's socket handshake. Fixed, so
// reconnects reuse one entry in the server's device list.
const embyDeviceID = "autothrottle"

// embyNowPlaying is what a session has loaded.
type embyNowPlaying struct {
	Name         string `json:"Name"`
	SeriesName   string `json:"SeriesName"`
	SeasonNumber *int   `json:"ParentIndexNumber"`
	Episode      *int   `json:"IndexNumber"`
	RunTimeTicks int64  `json:"RunTimeTicks"` // .NET ticks, not ms
}

// embySession is the subset of /Sessions this app reads.
type embySession struct {
	ID string `json:"Id"`
	// RemoteEndPoint is "IP" or "IP:port"; see embySessionIsLocal.
	RemoteEndPoint string `json:"RemoteEndPoint"`
	// NowPlayingItem is null for an idle client.
	NowPlayingItem *embyNowPlaying `json:"NowPlayingItem"`
	DeviceName     string          `json:"DeviceName"`
	PlayState      struct {
		IsPaused      bool  `json:"IsPaused"`
		PositionTicks int64 `json:"PositionTicks"`
	} `json:"PlayState"`
}

// embyTick is the duration of one .NET tick.
const embyTick = 100 * time.Nanosecond

func (s embySession) position() time.Duration {
	return time.Duration(s.PlayState.PositionTicks) * embyTick
}

func (s embySession) duration() time.Duration {
	if s.NowPlayingItem == nil {
		return 0
	}
	return time.Duration(s.NowPlayingItem.RunTimeTicks) * embyTick
}

// label renders what is playing in the same form as Plex's.
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

// embySessionIsLocal infers locality from the client's address being
// loopback, private or link-local, so a VPN client counts as local. An
// unparseable address is non-local: throttling when unsure only costs
// some speed.
func embySessionIsLocal(remoteEndPoint string) bool {
	host := strings.TrimSpace(remoteEndPoint)
	if host == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	// A bracketed IPv6 address with no port.
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

// testEmbyFlavor hits /System/Info, which needs the key — not the
// unauthenticated /System/Info/Public, which would pass any key.
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

// fetchEmbySessions reads /Sessions for the reconciliation poll.
func fetchEmbySessions(ctx context.Context, f embyFlavor, baseURL, apiKey string) ([]embySession, error) {
	var sessions []embySession
	err := getJSON(ctx, baseURL+"/Sessions", func(r *http.Request) {
		f.setAuth(r, apiKey)
	}, &sessions)
	if err != nil {
		return nil, err
	}
	return sessions, nil
}
