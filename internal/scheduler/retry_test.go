package scheduler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
)

// downLimiter is a download client that refuses until it is brought up,
// like one restarting for an update.
type downLimiter struct {
	mu    sync.Mutex
	up    bool
	tries int
}

func (d *downLimiter) SetLimits(context.Context, string, string, string, int64, int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tries++
	if !d.up {
		return errors.New("dial tcp 192.0.2.1:8080: connect: connection refused")
	}
	return nil
}

func (d *downLimiter) attempts() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tries
}

func (d *downLimiter) bringUp() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.up = true
}

func fastRetries(t *testing.T) {
	first, max, check := retryFirst, retryMax, retryCheck
	retryFirst, retryMax, retryCheck = 20*time.Millisecond, 40*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { retryFirst, retryMax, retryCheck = first, max, check })
}

// A client that was down when its rates were applied gets them once it
// is back, without anything else having to happen first.
func TestFailedApplyIsRetriedUntilItLands(t *testing.T) {
	fastRetries(t)
	s, database, _ := newTestScheduler(t)
	client := &downLimiter{}
	s.limiterFor = func(string) clients.RateLimiter { return client }
	_, dlID := addServerAndDownloader(t, database)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.RetryFailed(ctx)

	s.resolveNow()
	if s.State(dlID).Failure == "" {
		t.Fatal("the first apply did not fail")
	}
	eventually(t, "a few retries", func() bool { return client.attempts() >= 4 })

	// One error in the log, however many attempts it took.
	events, _ := database.ListEvents(50)
	errorsLogged := 0
	for _, e := range events {
		if e.Level == "error" {
			errorsLogged++
		}
	}
	if errorsLogged != 1 {
		t.Errorf("%d error events for one outage", errorsLogged)
	}

	client.bringUp()
	eventually(t, "the retry to land", func() bool { st := s.State(dlID); return st.Known && st.Failure == "" })
	// Back from a failure, the log names what was applied rather than
	// calling it an update.
	events, _ = database.ListEvents(1)
	if !strings.HasPrefix(events[0].Message, "restored default rate for qbit") {
		t.Errorf("recovery logged as %q", events[0].Message)
	}

	// Healthy, it stops trying.
	n := client.attempts()
	time.Sleep(100 * time.Millisecond)
	if client.attempts() != n {
		t.Errorf("%d more attempts while healthy", client.attempts()-n)
	}
}

func TestRetryIfFailing(t *testing.T) {
	s, database, _ := newTestScheduler(t)
	client := &downLimiter{}
	s.limiterFor = func(string) clients.RateLimiter { return client }
	_, dlID := addServerAndDownloader(t, database)
	s.resolveNow()

	client.bringUp()
	n := client.attempts()
	s.RetryIfFailing(dlID)
	eventually(t, "the re-apply", func() bool { return s.State(dlID).Failure == "" })

	// Nothing to do for one that is fine.
	n = client.attempts()
	s.RetryIfFailing(dlID)
	time.Sleep(50 * time.Millisecond)
	if client.attempts() != n {
		t.Error("a healthy downloader was re-applied")
	}
}
