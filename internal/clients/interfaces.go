package clients

import (
	"context"
	"fmt"
	"time"
)

// SupportedMediaServerType reports whether this build can watch a
// media server type. The database allows a wider set.
func SupportedMediaServerType(msType string) bool {
	switch msType {
	case "plex", "emby", "jellyfin":
		return true
	}
	return false
}

// TestMediaServer runs the connection test for a media server's type.
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

// SessionWatcher is one media server's live session feed, emitting an
// event when a session starts or stops counting. Implemented by
// PlexWatcher and EmbyWatcher (Emby and Jellyfin).
type SessionWatcher interface {
	// Run connects, and reconnects with backoff, until ctx is done.
	Run(ctx context.Context)
	// Events yields session state changes, local exemption applied.
	Events() <-chan SessionEvent
	// Snapshot returns every session playing, filtered as Events is. The
	// manager polls it to correct drift in the event stream.
	Snapshot(ctx context.Context) ([]Session, error)
	Connection() ConnectionState
}

// SessionEvent reports one session's throttle-relevant state.
type SessionEvent struct {
	Session
	Active bool
	// DetailOnly marks a change in how a session looks (position, pause)
	// but not whether it exists. These arrive every second or so, so
	// they must not re-resolve throttle state.
	DetailOnly bool
}

// Session is one playback session. Title, Device and the timing fields
// are for the UI only and best-effort: empty when the server did not
// report them.
type Session struct {
	Key    string
	Title  string
	Device string

	Position time.Duration
	// PositionAt is when Position was read. Servers report it every few
	// seconds at best, so without it a consumer re-reading a stale value
	// would step the clock backwards.
	PositionAt time.Time
	Duration   time.Duration
	// Paused still counts toward throttling (see pausedTracker); the page
	// needs it to stop the timecode.
	Paused bool
	// Local is informational; skip_local_playback decides whether a
	// local session counts.
	Local bool
}

// RateLimiter applies a download client's global speed limits, in KB/s;
// 0 means unlimited.
type RateLimiter interface {
	SetLimits(ctx context.Context, baseURL, username, password string, downloadLimitKBps, uploadLimitKBps int64) error
}

// SupportedDownloaderType reports whether this build can control a
// download client type.
func SupportedDownloaderType(dlType string) bool { return RateLimiterFor(dlType) != nil }

// DownloaderUsesUsername reports whether a download client type signs in
// with a username as well as a password. Deluge's Web UI has a password
// only.
func DownloaderUsesUsername(dlType string) bool { return dlType != "deluge" }

// TestDownloader runs the connection test for a downloader's type.
func TestDownloader(ctx context.Context, dlType, baseURL, username, password string) error {
	switch dlType {
	case "qbittorrent":
		return TestQBittorrent(ctx, baseURL, username, password)
	case "transmission":
		return TestTransmission(ctx, baseURL, username, password)
	case "deluge":
		return TestDeluge(ctx, baseURL, password)
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

// Deluge implements RateLimiter against the Deluge Web UI's JSON-RPC API.
// It has no username.
type Deluge struct{}

func (Deluge) SetLimits(ctx context.Context, baseURL, _, password string, downloadLimitKBps, uploadLimitKBps int64) error {
	return SetDelugeLimits(ctx, baseURL, password, downloadLimitKBps, uploadLimitKBps)
}

// RateLimiterFor resolves a downloader's type to its implementation,
// or nil for an unsupported one.
func RateLimiterFor(downloaderType string) RateLimiter {
	switch downloaderType {
	case "qbittorrent":
		return QBittorrent{}
	case "transmission":
		return Transmission{}
	case "deluge":
		return Deluge{}
	default:
		return nil
	}
}
