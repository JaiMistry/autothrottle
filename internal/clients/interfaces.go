package clients

import (
	"context"
	"fmt"
	"time"
)

// SupportedMediaServerType reports whether this build can watch a
// media server type. The database's CHECK constraint is deliberately
// wider, so this is what the web layer validates against.
func SupportedMediaServerType(msType string) bool {
	switch msType {
	case "plex", "emby", "jellyfin":
		return true
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
	// Snapshot returns every session currently playing, with the same
	// exemption filtering Events applies. The event stream alone can
	// drift out of sync with reality — a WebSocket that drops
	// mid-playback may never re-send a "playing" notification once
	// reconnected, leaving a session tracked as active forever — so
	// the manager polls this as the authoritative correction.
	Snapshot(ctx context.Context) ([]Session, error)
	// Connection reports whether the live feed is up, since when, and
	// when the next attempt is due.
	Connection() ConnectionState
}

// SessionEvent reports one session's throttle-relevant state.
type SessionEvent struct {
	Session
	Active bool
	// DetailOnly marks an update that changes what a session looks like
	// — how far in it is, whether it is paused — without changing
	// whether it exists. These arrive every second or so from a live
	// socket, so they must not re-resolve throttle state: that would
	// mean a downloader API call per tick for no change in outcome.
	DetailOnly bool
}

// Session identifies one playback session and what it is.
//
// Title and Device are for the UI only — nothing about throttling
// depends on them. They are best-effort: a server may not report
// either, and the interface says so rather than inventing a
// placeholder, because "Unknown — Unknown device" reads as a bug where
// an absent line reads as an absent fact.
type Session struct {
	Key    string
	Title  string
	Device string
	// Position and Duration describe where playback has reached. Both
	// are zero when the server did not report them; the UI draws no
	// progress bar in that case rather than guessing at one.
	Position time.Duration
	// PositionAt is when Position was observed. Media servers report a
	// position every few seconds at best — Plex in roughly ten-second
	// steps — so a reading is usually already stale by the time anything
	// renders it. Without knowing how stale, a consumer cannot tell a
	// fresh reading from an old one and will step the clock backwards
	// every time it re-reads the same value.
	PositionAt time.Time
	Duration   time.Duration
	// Paused is what the server last reported. A paused session still
	// counts toward throttling — see pausedTracker — but the page needs
	// to know, so it does not advance a timecode that is standing still.
	Paused bool
	// Local reports playback from the same network as the server. It is
	// informational here — whether a local session counts toward
	// throttling is decided by the server's skip_local_playback setting,
	// not by this field.
	Local bool
}

// RateLimiter applies a download client's global speed limits. Limits
// are KB/s, matching the app's schema convention; 0 means unlimited.
//
// Implemented by QBittorrent and Transmission. The scheduler resolves
// one of these by downloader type and never names a concrete client.
type RateLimiter interface {
	SetLimits(ctx context.Context, baseURL, username, password string, downloadLimitKBps, uploadLimitKBps int64) error
}

// SupportedDownloaderType reports whether this build can control a
// download client type.
func SupportedDownloaderType(dlType string) bool { return RateLimiterFor(dlType) != nil }

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

// QBittorrent implements RateLimiter against qBittorrent's WebUI API.
type QBittorrent struct{}

func (QBittorrent) SetLimits(ctx context.Context, baseURL, username, password string, downloadLimitKBps, uploadLimitKBps int64) error {
	return SetQBittorrentLimits(ctx, baseURL, username, password, downloadLimitKBps, uploadLimitKBps)
}

// Transmission implements RateLimiter against Transmission's JSON-RPC API.
type Transmission struct{}

func (Transmission) SetLimits(ctx context.Context, baseURL, username, password string, downloadLimitKBps, uploadLimitKBps int64) error {
	return SetTransmissionLimits(ctx, baseURL, username, password, downloadLimitKBps, uploadLimitKBps)
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
