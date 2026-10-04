package auth

import (
	"sync"
	"time"
)

// LoginLimiter is a simple sliding-window rate limiter for the login
// endpoint. A single global window (not per-IP) is deliberate: this is
// a single-admin tool behind a home network, not a multi-tenant
// service, so the goal is just blocking brute-force guessing, not
// per-client fairness.
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

// Allow reports whether another login attempt is permitted right now,
// and records this attempt if so.
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
