package clients

import "context"

// Limits is a download and upload rate pair in KB/s; 0 is unlimited.
type Limits struct {
	DownloadKBps, UploadKBps int64
}

// ClientState is what a download client reports about its own limits.
type ClientState struct {
	// Normal is the client's ordinary global limits.
	Normal Limits
	// Alternative is its second set, and AlternativeOn whether that set is
	// in force. Both zero for a client without one.
	Alternative   Limits
	AlternativeOn bool
	// Scheduler is the client's own speed scheduler, in the client's
	// words, when it is on and set to change speeds; empty otherwise. It
	// fights AutoThrottle over the limits.
	Scheduler string
}

// Desired is the state AutoThrottle wants a client in. Normal is always
// written. Alternative and AlternativeOn are written only when set, and
// only by clients that have alternative speed.
type Desired struct {
	Normal        Limits
	Alternative   *Limits
	AlternativeOn *bool
}

// Controller is a download client AutoThrottle can read back and fully
// manage, beyond setting its limits.
type Controller interface {
	RateLimiter
	// Inspect reads the client's limits, alternative speed and scheduler.
	Inspect(ctx context.Context, baseURL, username, password string) (ClientState, error)
	// Apply puts the client into the desired state.
	Apply(ctx context.Context, baseURL, username, password string, want Desired) error
	// ClearConflicts turns off what stops AutoThrottle's limits from
	// applying: the client's own speed scheduler (its settings kept) and
	// alternative speed.
	ClearConflicts(ctx context.Context, baseURL, username, password string) error
}

// ControllerFor resolves a downloader's type to its Controller, or nil
// when the type can't be fully managed.
func ControllerFor(downloaderType string) Controller {
	if c, ok := RateLimiterFor(downloaderType).(Controller); ok {
		return c
	}
	return nil
}

// Matches reports whether a client's state is what was desired, so a
// difference means something else changed it.
func (want Desired) Matches(got ClientState) bool {
	if got.Normal != want.Normal {
		return false
	}
	if want.Alternative != nil && got.Alternative != *want.Alternative {
		return false
	}
	if want.AlternativeOn != nil && got.AlternativeOn != *want.AlternativeOn {
		return false
	}
	return true
}
