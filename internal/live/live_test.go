package live

import (
	"slices"
	"sync"
	"testing"
	"time"
)

func woke(s *Subscription) bool {
	select {
	case <-s.Wake():
		return true
	case <-time.After(time.Second):
		return false
	}
}

func TestNotifyReachesEverySubscriber(t *testing.T) {
	h := NewHub()
	a, b := h.Subscribe(), h.Subscribe()
	h.Notify(Dashboard)
	for _, s := range []*Subscription{a, b} {
		if !woke(s) || !slices.Equal(s.Take(), []Topic{Dashboard}) {
			t.Fatal("a subscriber missed the change")
		}
	}
}

// A burst is one wake-up naming every topic, not one per change.
func TestBurstsCollapse(t *testing.T) {
	h := NewHub()
	s := h.Subscribe()
	for range 50 {
		h.Notify(Logs)
		h.Notify(Dashboard)
	}
	h.Notify(History)
	if !woke(s) {
		t.Fatal("no wake-up")
	}
	if got := s.Take(); !slices.Equal(got, []Topic{Dashboard, History, Logs}) {
		t.Errorf("took %v", got)
	}
	select {
	case <-s.Wake():
		t.Error("a second wake-up for the same burst")
	default:
	}
	if got := s.Take(); len(got) != 0 {
		t.Errorf("topics left after Take: %v", got)
	}
}

func TestCloseEndsStreams(t *testing.T) {
	h := NewHub()
	s := h.Subscribe()
	gone := h.Subscribe()
	gone.Close()
	gone.Close() // twice is harmless
	if h.Subscribers() != 1 {
		t.Fatalf("%d subscribers after one closed", h.Subscribers())
	}
	h.Close()
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("closing the hub left a stream open")
	}
	s.Close() // after the hub closed it
	late := h.Subscribe()
	select {
	case <-late.Done():
	default:
		t.Error("a subscription on a closed hub is not done")
	}
	h.Notify(Dashboard) // nothing to reach, and no panic
}

func TestNilNotifier(t *testing.T) {
	Notify(nil, Dashboard)
}

func TestConcurrentUse(t *testing.T) {
	h := NewHub()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			s := h.Subscribe()
			for range 100 {
				s.Take()
			}
			s.Close()
		}()
		go func() {
			defer wg.Done()
			for range 100 {
				h.Notify(Logs)
			}
		}()
	}
	wg.Wait()
	h.Close()
}
