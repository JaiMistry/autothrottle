package clients

import (
	"context"
	"fmt"
)

// MediaServerType names one supported media server, for a UI that
// renders the set generically rather than hardcoding an option per
// type — the same reason internal/settings exposes Definitions.
type MediaServerType struct {
	Value string
	Label string
}

// MediaServerTypes lists every type with a working watcher, in the
// order a UI should offer them.
var MediaServerTypes = []MediaServerType{
	{Value: "plex", Label: "Plex"},
	{Value: "emby", Label: "Emby"},
	{Value: "jellyfin", Label: "Jellyfin"},
}

// SupportedMediaServerType reports whether a type string names a media
// server this build can actually watch. The database's CHECK
// constraint is deliberately wider than this — it permits any type the
// schema anticipates — so this is what the web layer validates against.
func SupportedMediaServerType(msType string) bool {
	for _, t := range MediaServerTypes {
		if t.Value == msType {
			return true
		}
	}
	return false
}

// TestMediaServer runs the connection test for a media server's type.
//
// Worth knowing that these are not equally strong: the Emby and
// Jellyfin checks genuinely validate the API key, while the Plex one
// cannot on a LAN — see TestPlex's comment for why.
func TestMediaServer(ctx context.Context, msType, baseURL, token string) error {
	switch msType {
	case "plex":
		return TestPlex(ctx, baseURL, token)
	case "emby":
		return TestEmby(ctx, baseURL, token)
	case "jellyfin":
		return TestJellyfin(ctx, baseURL, token)
	default:
		return fmt.Errorf("unsupported media server type %q", msType)
	}
}

// SessionWatcher is one media server's live session feed: a long-lived
// connection that emits an event whenever a session starts or stops
// counting toward "this server has something playing."
//
// Implemented by PlexWatcher and by EmbyWatcher, which covers both
// Emby and Jellyfin. The manager and the scheduler see only this
// interface, so the two protocols' substantial differences — Plex
// pushes state changes, the others push whole session lists on a
// subscription — stay contained in the clients package.
type SessionWatcher interface {
	// Run connects, and reconnects with backoff, until ctx is done.
	Run(ctx context.Context)
	// Events yields session state changes. Local-playback exemption is
	// already applied — an exempted session never appears here.
	Events() <-chan SessionEvent
	// Snapshot returns every session key currently playing, with the
	// same exemption filtering Events applies. The event stream alone
	// can drift out of sync with reality — a WebSocket that drops
	// mid-playback may never re-send a "playing" notification once
	// reconnected, leaving a session tracked as active forever — so
	// the manager polls this as the authoritative correction.
	Snapshot(ctx context.Context) ([]string, error)
}

// SessionEvent reports one session's throttle-relevant state.
type SessionEvent struct {
	SessionKey string
	Active     bool
}

// RateLimiter applies a download client's global speed limits. Limits
// are KB/s, matching the app's schema convention; 0 means unlimited.
//
// Implemented by QBittorrent and Transmission. The scheduler resolves
// one of these by downloader type and never names a concrete client.
type RateLimiter interface {
	SetLimits(ctx context.Context, baseURL, username, password string, downloadLimitKBps, uploadLimitKBps int64) error
}

// RateReporter reads a download client's current transfer rates, in
// bytes per second. Separate from RateLimiter because reading and
// writing rates are independent capabilities: a client could plausibly
// support one without the other, and the bandwidth sampler only ever
// needs to read.
type RateReporter interface {
	Rates(ctx context.Context, baseURL, username, password string) (downloadBytesPerSec, uploadBytesPerSec int64, err error)
}

// DownloaderType names one supported download client, for a UI that
// renders the set generically. Mirrors MediaServerTypes.
type DownloaderType struct {
	Value string
	Label string
}

// DownloaderTypes lists every type with a working rate limiter, in the
// order a UI should offer them.
var DownloaderTypes = []DownloaderType{
	{Value: "qbittorrent", Label: "qBittorrent"},
	{Value: "transmission", Label: "Transmission"},
}

// SupportedDownloaderType reports whether a type string names a
// download client this build can actually control.
func SupportedDownloaderType(dlType string) bool {
	for _, t := range DownloaderTypes {
		if t.Value == dlType {
			return true
		}
	}
	return false
}

// TestDownloader runs the connection test for a downloader's type.
func TestDownloader(ctx context.Context, dlType, baseURL, username, password string) error {
	switch dlType {
	case "qbittorrent":
		return TestQBittorrent(ctx, baseURL, username, password)
	case "transmission":
		return TestTransmission(ctx, baseURL, username, password)
	default:
		return fmt.Errorf("unsupported downloader type %q", dlType)
	}
}

// QBittorrent implements RateLimiter and RateReporter against
// qBittorrent's WebUI API.
type QBittorrent struct{}

func (QBittorrent) SetLimits(ctx context.Context, baseURL, username, password string, downloadLimitKBps, uploadLimitKBps int64) error {
	return SetQBittorrentLimits(ctx, baseURL, username, password, downloadLimitKBps, uploadLimitKBps)
}

func (QBittorrent) Rates(ctx context.Context, baseURL, username, password string) (int64, int64, error) {
	return QBittorrentRates(ctx, baseURL, username, password)
}

// Transmission implements RateLimiter and RateReporter against
// Transmission's JSON-RPC API.
type Transmission struct{}

func (Transmission) SetLimits(ctx context.Context, baseURL, username, password string, downloadLimitKBps, uploadLimitKBps int64) error {
	return SetTransmissionLimits(ctx, baseURL, username, password, downloadLimitKBps, uploadLimitKBps)
}

func (Transmission) Rates(ctx context.Context, baseURL, username, password string) (int64, int64, error) {
	return TransmissionRates(ctx, baseURL, username, password)
}

// RateLimiterFor resolves a downloader's type to its implementation.
// Returns nil for an unsupported type; callers report that as a
// configuration error rather than silently doing nothing.
func RateLimiterFor(downloaderType string) RateLimiter {
	switch downloaderType {
	case "qbittorrent":
		return QBittorrent{}
	case "transmission":
		return Transmission{}
	default:
		return nil
	}
}

// RateReporterFor resolves a downloader's type to its rate-reading
// implementation. Nil means "no bandwidth data available for this
// type" — the sampler skips it rather than treating it as an error.
func RateReporterFor(downloaderType string) RateReporter {
	switch downloaderType {
	case "qbittorrent":
		return QBittorrent{}
	case "transmission":
		return Transmission{}
	default:
		return nil
	}
}
