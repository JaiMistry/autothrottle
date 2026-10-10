package settings

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/db"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return New(database)
}

// Off on a fresh install, and the switch survives a restart.
func TestPausedRoundTrips(t *testing.T) {
	s := openStore(t)
	if s.Paused() {
		t.Error("a fresh install starts with throttling switched off")
	}
	for _, want := range []bool{true, false, true} {
		if err := s.SetPaused(want); err != nil {
			t.Fatalf("SetPaused(%v): %v", want, err)
		}
		if got := New(s.db).Paused(); got != want {
			t.Errorf("after SetPaused(%v), a fresh store reads %v", want, got)
		}
	}
}

func TestUnreadablePauseValueIsOff(t *testing.T) {
	s := openStore(t)
	if err := s.db.SetSetting(keyPaused, "maybe"); err != nil {
		t.Fatal(err)
	}
	if s.Paused() {
		t.Error("an unreadable value must fall back to throttling on")
	}
}

func TestPausedSince(t *testing.T) {
	s := openStore(t)
	if _, ok := s.PausedSince(); ok {
		t.Fatal("a time before ever pausing")
	}
	before := time.Now().Add(-time.Second)
	if err := s.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	first, ok := s.PausedSince()
	if !ok || first.Before(before) || first.After(time.Now().Add(time.Second)) {
		t.Fatalf("paused since %v, %v", first, ok)
	}
	// Disabling again while disabled keeps the original time.
	time.Sleep(1100 * time.Millisecond)
	_ = s.SetPaused(true)
	if again, _ := New(s.db).PausedSince(); !again.Equal(first) {
		t.Errorf("the time moved from %v to %v", first, again)
	}
	_ = s.SetPaused(false)
	if _, ok := s.PausedSince(); ok {
		t.Error("a time while resumed")
	}
}
