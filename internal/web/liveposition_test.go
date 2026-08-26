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
			// The case the jumping clock came from: a reading taken four
			// seconds ago has to come back four seconds further on, or a
			// page that ticks in between must step backwards to match it.
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
			// A session the event stream announced before any snapshot
			// described it has a position but no timestamp to age it by.
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

// Two reads of the same stored reading, a second apart, must differ by
// a second — that monotonicity is the whole point.
func TestLivePositionIsMonotonicBetweenReports(t *testing.T) {
	base := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	session := clients.Session{Position: time.Minute, PositionAt: base, Duration: time.Hour}

	prev := livePosition(session, base)
	for i := 1; i <= 10; i++ {
		got := livePosition(session, base.Add(time.Duration(i)*time.Second))
		if got <= prev {
			t.Fatalf("position went backwards at +%ds: %v then %v", i, prev, got)
		}
		prev = got
	}
}
