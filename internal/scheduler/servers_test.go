package scheduler

import (
	"context"
	"fmt"
	"testing"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/db"
)

// Servers of one type number their sessions alike, every Plex from "1",
// so three Plex servers on one downloader each keep their own: one
// ending, or one's snapshot, or losing one, leaves the others' alone.
func TestServersOfOneTypeKeepTheirOwnSessions(t *testing.T) {
	s, database, limiter := newTestScheduler(t)
	_, dlID := addServerAndDownloader(t, database)
	servers := make([]int64, 3)
	for i := range servers {
		id, err := database.CreateMediaServer(db.MediaServer{
			Name: fmt.Sprintf("Plex %d", i+1), Type: "plex", URL: fmt.Sprintf("http://plex%d.invalid:32400", i+1),
			CredentialEncrypted: "unused-in-these-tests", Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.CreateMapping(id, dlID); err != nil {
			t.Fatal(err)
		}
		servers[i] = id
	}
	ctx := context.Background()
	rate := func() int64 {
		s.resolve(ctx)
		calls := limiter.snapshot()
		return calls[len(calls)-1].download
	}
	counts := func() [3]int {
		return [3]int{s.ActiveSessionCount(servers[0]), s.ActiveSessionCount(servers[1]), s.ActiveSessionCount(servers[2])}
	}

	for _, id := range servers {
		s.HandleSessionEvent(id, clients.Session{Key: "1", Title: "Andor S1E4", Device: "TV"}, true)
	}
	if got := counts(); got != [3]int{1, 1, 1} || rate() != 500 {
		t.Fatalf("all playing: %v sessions, rate %d", got, rate())
	}

	s.HandleSessionEvent(servers[0], clients.Session{Key: "1"}, false)
	s.ReplaceSessions(servers[1], nil)
	if got := counts(); got != [3]int{0, 0, 1} || rate() != 500 {
		t.Fatalf("one still playing: %v sessions, rate %d", got, rate())
	}
	for _, id := range servers[:2] {
		if open, err := database.OpenSessions(id); err != nil || len(open) != 0 {
			t.Errorf("server %d's history still open: %v, %v", id, open, err)
		}
	}
	if open, err := database.OpenSessions(servers[2]); err != nil || len(open) != 1 {
		t.Errorf("the playing server's history: %v, %v", open, err)
	}

	s.ClearSessions(servers[2])
	if got := counts(); got != [3]int{0, 0, 0} || rate() != 0 {
		t.Fatalf("none playing: %v sessions, rate %d", got, rate())
	}
}
