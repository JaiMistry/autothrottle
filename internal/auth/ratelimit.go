package auth

import (
	"sync"
	"time"
)

// LoginLimiter is one sliding window for the whole app, not per IP: a
// single-admin tool needs only to stop brute-force guessing.
type LoginLimiter struct {
	mu       sync.Mutex
	attempts []time.Time
	max      int
	window   time.Duration
}

// NewLoginLimiter allows 5 attempts a minute.
func NewLoginLimiter() *LoginLimiter {
	return newLoginLimiter(loginRateMax, loginRateWindow)
}

func newLoginLimiter(max int, window time.Duration) *LoginLimiter {
	return &LoginLimiter{max: max, window: window}
}

// Allow reports whether another attempt is permitted, recording it if so.
func (l *LoginLimiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-l.window)

	live := l.attempts[:0]
	for _, t := range l.attempts {
		if t.After(cutoff) {
			live = append(live, t)
		}
	}
	l.attempts = live

	if len(l.attempts) >= l.max {
		return false
	}
	l.attempts = append(l.attempts, now)
	return true
}
