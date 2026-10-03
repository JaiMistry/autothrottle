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

	// PausedSessionTimeout caps how long a continuously paused stream
	// keeps downloads throttled before it is treated as abandoned.
	PausedSessionTimeout = 30 * time.Minute

	// BandwidthSampleInterval is how often each downloader's transfer
	// rates are recorded.
	BandwidthSampleInterval = 60 * time.Second

	// BandwidthRawRetention keeps full-resolution samples a little over
	// a week, so a 7-day view is full resolution end to end.
	BandwidthRawRetention = 8 * 24 * time.Hour

	// BandwidthRollupRetention keeps the hourly aggregates.
	BandwidthRollupRetention = 90 * 24 * time.Hour

	// EventRetention keeps the event log and session history.
	EventRetention = 90 * 24 * time.Hour

	// MaintenanceInterval is how often retention and rollups run.
	MaintenanceInterval = time.Hour
)

// keyPaused is the only value this package persists.
const keyPaused = "throttle.paused"

// Store is the persisted half of the settings: the pause switch. The
// interval methods report the constants above, which keeps every
// caller reading its timing from one place.
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

func (s *Store) SessionReconcileInterval() time.Duration { return SessionReconcileInterval }
func (s *Store) PausedSessionTimeout() time.Duration     { return PausedSessionTimeout }
func (s *Store) BandwidthSampleInterval() time.Duration  { return BandwidthSampleInterval }
func (s *Store) MaintenanceInterval() time.Duration      { return MaintenanceInterval }
func (s *Store) BandwidthRawRetention() time.Duration    { return BandwidthRawRetention }
func (s *Store) BandwidthRollupRetention() time.Duration { return BandwidthRollupRetention }
func (s *Store) EventRetention() time.Duration           { return EventRetention }
