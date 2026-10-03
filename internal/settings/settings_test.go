package settings

import (
	"path/filepath"
	"testing"

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

func TestPausedDefaultsOff(t *testing.T) {
	if openStore(t).Paused() {
		t.Error("a fresh install must not start with throttling switched off")
	}
}

// The switch has to survive a restart: someone who turned throttling
// off before rebooting the host should not find it silently back on.
func TestPausedRoundTrips(t *testing.T) {
	s := openStore(t)
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
