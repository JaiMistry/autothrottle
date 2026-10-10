package web

import (
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
)

func TestLivePosition(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	const dur = 30 * time.Minute

	cases := []struct {
		name    string
		session clients.Session
		want    time.Duration
	}{
		{
			// Otherwise a page that ticked in between steps backwards.
			name:    "advances a stale reading",
			session: clients.Session{Position: 4 * time.Minute, PositionAt: now.Add(-4 * time.Second), Duration: dur},
			want:    4*time.Minute + 4*time.Second,
		},
		{
			name:    "a paused session does not move",
			session: clients.Session{Position: 4 * time.Minute, PositionAt: now.Add(-30 * time.Second), Duration: dur, Paused: true},
			want:    4 * time.Minute,
		},
		{
			// Announced by the event stream, not yet described by a snapshot.
			name:    "no timestamp is left alone",
			session: clients.Session{Position: 4 * time.Minute, Duration: dur},
			want:    4 * time.Minute,
		},
		{
			name:    "never runs past the end",
			session: clients.Session{Position: dur - time.Second, PositionAt: now.Add(-time.Hour), Duration: dur},
			want:    dur,
		},
	}

	for _, c := range cases {
		if got := livePosition(c.session, now); got != c.want {
			t.Errorf("%s: livePosition = %v, want %v", c.name, got, c.want)
		}
	}
}
