// Package settings holds how AutoThrottle runs internally, and the one
// thing about that a user changes from the UI: whether throttling is
// switched off.
//
// The intervals and retention windows are fixed. They are tuning for
// the app's own machinery — how often it double-checks a media server,
// how long history is kept — and none of them is a decision a person
// installing a bandwidth throttler should have to make.
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

	// HeartbeatInterval is how often a running process records that it
	// is still alive, which bounds how precisely the throttle history
	// knows when it stopped.
	HeartbeatInterval = time.Minute

	// EventRetention keeps the event log, session history and throttle
	// history.
	EventRetention = 90 * 24 * time.Hour

	// MaintenanceInterval is how often retention runs.
	MaintenanceInterval = time.Hour
)

// keyPaused is the only value this package persists.
const keyPaused = "throttle.paused"

// Store persists the pause switch.
type Store struct {
	db *db.DB
}

func New(database *db.DB) *Store { return &Store{db: database} }

// Paused reports the manual "pause all throttling" override. It's
// persisted rather than held in memory so it survives a restart: a
// user who paused throttling before rebooting the host should not come
// back to it silently re-enabled.
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
	return s.db.SetSetting(keyPaused, strconv.FormatBool(paused))
}
