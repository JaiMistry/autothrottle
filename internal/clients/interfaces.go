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
// Transmission slots in here as a second implementation — the
// scheduler resolves one of these by downloader type and never names a
// concrete client.
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

// QBittorrent implements RateLimiter and RateReporter against
// qBittorrent's WebUI API.
type QBittorrent struct{}

func (QBittorrent) SetLimits(ctx context.Context, baseURL, username, password string, downloadLimitKBps, uploadLimitKBps int64) error {
	return SetQBittorrentLimits(ctx, baseURL, username, password, downloadLimitKBps, uploadLimitKBps)
}

func (QBittorrent) Rates(ctx context.Context, baseURL, username, password string) (int64, int64, error) {
	return QBittorrentRates(ctx, baseURL, username, password)
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

// RateReporterFor resolves a downloader's type to its rate-reading
// implementation. Nil means "no bandwidth data available for this
// type" — the sampler skips it rather than treating it as an error.
func RateReporterFor(downloaderType string) RateReporter {
	switch downloaderType {
	case "qbittorrent":
		return QBittorrent{}
	default:
		return nil
	}
}
