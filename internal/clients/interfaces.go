package clients

import "context"

// SessionWatcher is one media server's live session feed: a long-lived
// connection that emits an event whenever a session starts or stops
// counting toward "this server has something playing."
//
// Plex is the only implementation in v1. Emby and Jellyfin both expose
// the same shape of API (a WebSocket plus a SessionsStart subscribe
// message), so they land as additional implementations of this
// interface rather than as changes to anything downstream of it —
// the watcher manager and the scheduler only ever see this interface.
type SessionWatcher interface {
	// Run connects, and reconnects with backoff, until ctx is done.
	Run(ctx context.Context)
	// Events yields session state changes. Local-playback exemption is
	// already applied — an exempted session never appears here.
	Events() <-chan SessionEvent
}

// SessionEvent reports one session's throttle-relevant state.
type SessionEvent struct {
	SessionKey string
	Active     bool
}

// RateLimiter applies a download client's global speed limits. Limits
// are KB/s, matching the app's schema convention; 0 means unlimited.
//
// Transmission slots in here as a second implementation — the
// scheduler resolves one of these by downloader type and never names a
// concrete client.
type RateLimiter interface {
	SetLimits(ctx context.Context, baseURL, username, password string, downloadLimitKBps, uploadLimitKBps int64) error
}

// QBittorrent implements RateLimiter against qBittorrent's WebUI API.
type QBittorrent struct{}

func (QBittorrent) SetLimits(ctx context.Context, baseURL, username, password string, downloadLimitKBps, uploadLimitKBps int64) error {
	return SetQBittorrentLimits(ctx, baseURL, username, password, downloadLimitKBps, uploadLimitKBps)
}

// RateLimiterFor resolves a downloader's type to its implementation.
// Returns nil for an unsupported type; callers report that as a
// configuration error rather than silently doing nothing.
func RateLimiterFor(downloaderType string) RateLimiter {
	switch downloaderType {
	case "qbittorrent":
		return QBittorrent{}
	default:
		return nil
	}
}
