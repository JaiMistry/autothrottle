// Package settings holds AutoThrottle's fixed internal timings, and the
// one thing a user switches from the UI: whether throttling is off.
package settings

import (
	"log/slog"
	"strconv"
	"time"

	"github.com/JaiMistry/autothrottle/internal/db"
)

const (
	// SessionReconcileInterval is how often each media server's session
	// list is re-read to correct anything the live feed missed.
	SessionReconcileInterval = 60 * time.Second

	// HeartbeatInterval is how often a running process records it is
	// alive, which bounds how precisely history knows when it stopped.
	HeartbeatInterval = time.Minute

	// EventRetention keeps the event log, session history and throttle
	// history.
	EventRetention = 90 * 24 * time.Hour

	// MaintenanceInterval is how often retention runs.
	MaintenanceInterval = time.Hour

	// RestoreDelay holds a throttle this long after playback ends before
	// restoring default rates, so autoplay's next episode, a few seconds
	// later, doesn't flip the downloaders off and back on.
	RestoreDelay = 30 * time.Second
)

// The pause switch, and when it was last switched on.
const (
	keyPaused      = "throttle.paused"
	keyPausedSince = "throttle.paused_since"
)

// Store persists the pause switch.
type Store struct {
	db *db.DB
}

func New(database *db.DB) *Store { return &Store{db: database} }

// Paused reports the pause-all override, persisted so a restart does
// not silently re-enable throttling.
func (s *Store) Paused() bool {
	raw, ok, err := s.db.GetSetting(keyPaused)
	if err != nil {
		slog.Error("settings: reading the pause switch", "error", err)
		return false
	}
	if !ok {
		return false
	}
	paused, err := strconv.ParseBool(raw)
	if err != nil {
		slog.Warn("settings: unreadable pause switch, treating as off", "value", raw)
		return false
	}
	return paused
}

func (s *Store) SetPaused(paused bool) error {
	// Switching off again while off keeps the original time.
	if paused && !s.Paused() {
		if err := s.db.SetSetting(keyPausedSince, time.Now().UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return s.db.SetSetting(keyPaused, strconv.FormatBool(paused))
}

// PausedSince is when throttling was disabled; ok is false while it is
// not, or if the time was never recorded.
func (s *Store) PausedSince() (since time.Time, ok bool) {
	if !s.Paused() {
		return time.Time{}, false
	}
	raw, found, err := s.db.GetSetting(keyPausedSince)
	if err != nil || !found {
		return time.Time{}, false
	}
	since, err = time.Parse(time.RFC3339, raw)
	return since, err == nil
}
