// Package settings owns the app's runtime-adjustable knobs: the keys,
// their types, their defaults, and the parsing of the text values the
// settings table stores.
//
// Everything here is read at the point of use rather than cached at
// startup, so a change takes effect on the next tick without a
// restart — the same property configuration changes already have.
package settings

import (
	"log/slog"
	"strconv"
	"time"

	"github.com/JaiMistry/autothrottle/internal/db"
)

// Keys. Namespaced by area so a settings dump reads as grouped.
const (
	KeyPaused                    = "throttle.paused"
	KeySessionReconcileInterval  = "sessions.reconcile_interval_seconds"
	KeyPausedSessionTimeout      = "sessions.paused_timeout_seconds"
	KeyBandwidthSampleInterval   = "bandwidth.sample_interval_seconds"
	KeyBandwidthRawRetentionDays = "bandwidth.raw_retention_days"
	KeyBandwidthRollupRetention  = "bandwidth.rollup_retention_days"
	KeyEventRetentionDays        = "events.retention_days"
	KeyMaintenanceInterval       = "maintenance.interval_seconds"
)

// Defaults, applied whenever a key has never been written.
const (
	DefaultSessionReconcileInterval  = 60 * time.Second
	DefaultPausedSessionTimeout      = 30 * time.Minute
	DefaultBandwidthSampleInterval   = 60 * time.Second
	DefaultBandwidthRawRetentionDays = 8
	DefaultBandwidthRollupRetention  = 90
	DefaultEventRetentionDays        = 90
	DefaultMaintenanceInterval       = time.Hour
)

// Definition describes one setting for a UI that renders the set
// generically instead of hardcoding a field per knob.
type Definition struct {
	Key         string
	Label       string
	Description string
	Unit        string // "seconds" or "days"
	Default     string
	Min, Max    int64
}

// Definitions lists every numeric setting, in the order a UI should
// show them. The pause override is deliberately absent: it's a live
// dashboard control, not a configuration field.
var Definitions = []Definition{
	{
		Key: KeySessionReconcileInterval, Label: "Session reconcile interval",
		Description: "How often to re-check each media server's live sessions against what the watcher believes is playing.",
		Unit:        "seconds", Default: "60", Min: 10, Max: 3600,
	},
	{
		Key: KeyPausedSessionTimeout, Label: "Paused session timeout",
		Description: "How long a paused stream keeps downloads throttled before it is treated as abandoned. A pause does not end a stream, so it keeps counting until this expires.",
		Unit:        "seconds", Default: "1800", Min: 60, Max: 86400,
	},
	{
		Key: KeyBandwidthSampleInterval, Label: "Bandwidth sample interval",
		Description: "How often to record each downloader's current transfer rates.",
		Unit:        "seconds", Default: "60", Min: 10, Max: 3600,
	},
	{
		Key: KeyBandwidthRawRetentionDays, Label: "Raw sample retention",
		Description: "How long full-resolution bandwidth samples are kept before being rolled up into hourly averages. Slightly longer than a week so a 7-day chart is always full resolution end to end.",
		Unit:        "days", Default: "8", Min: 1, Max: 365,
	},
	{
		Key: KeyBandwidthRollupRetention, Label: "Hourly rollup retention",
		Description: "How long hourly bandwidth aggregates are kept.",
		Unit:        "days", Default: "90", Min: 1, Max: 3650,
	},
	{
		Key: KeyEventRetentionDays, Label: "History retention",
		Description: "How long history events are kept before being deleted.",
		Unit:        "days", Default: "90", Min: 1, Max: 3650,
	},
	{
		Key: KeyMaintenanceInterval, Label: "Maintenance interval",
		Description: "How often retention and rollup maintenance runs.",
		Unit:        "seconds", Default: "3600", Min: 60, Max: 86400,
	},
}

// Store reads and writes settings, falling back to the defaults above
// for anything unset or unparseable.
type Store struct {
	db *db.DB
}

func New(database *db.DB) *Store { return &Store{db: database} }

// Paused reports the manual "pause all throttling" override. It's
// persisted rather than held in memory so it survives a restart: a
// user who paused throttling before rebooting the host should not come
// back to it silently re-enabled.
func (s *Store) Paused() bool { return s.boolValue(KeyPaused, false) }

func (s *Store) SetPaused(paused bool) error {
	return s.db.SetSetting(KeyPaused, strconv.FormatBool(paused))
}

func (s *Store) SessionReconcileInterval() time.Duration {
	return s.duration(KeySessionReconcileInterval, DefaultSessionReconcileInterval)
}

// PausedSessionTimeout caps how long a continuously paused session
// goes on counting toward throttling.
func (s *Store) PausedSessionTimeout() time.Duration {
	return s.duration(KeyPausedSessionTimeout, DefaultPausedSessionTimeout)
}

func (s *Store) BandwidthSampleInterval() time.Duration {
	return s.duration(KeyBandwidthSampleInterval, DefaultBandwidthSampleInterval)
}

func (s *Store) MaintenanceInterval() time.Duration {
	return s.duration(KeyMaintenanceInterval, DefaultMaintenanceInterval)
}

func (s *Store) BandwidthRawRetention() time.Duration {
	return s.days(KeyBandwidthRawRetentionDays, DefaultBandwidthRawRetentionDays)
}

func (s *Store) BandwidthRollupRetention() time.Duration {
	return s.days(KeyBandwidthRollupRetention, DefaultBandwidthRollupRetention)
}

func (s *Store) EventRetention() time.Duration {
	return s.days(KeyEventRetentionDays, DefaultEventRetentionDays)
}

// All returns every setting's current effective value, defaults
// included, for a configuration page rendering the whole set.
func (s *Store) All() map[string]string {
	out := make(map[string]string, len(Definitions))
	for _, def := range Definitions {
		out[def.Key] = strconv.FormatInt(s.intValue(def.Key, mustParse(def.Default)), 10)
	}
	return out
}

// Set writes a value after validating it against the key's definition.
// Unknown keys are rejected so a typo can't quietly persist a setting
// nothing will ever read.
func (s *Store) Set(key, value string) error {
	if key == KeyPaused {
		paused, err := strconv.ParseBool(value)
		if err != nil {
			return errInvalid(key, "must be true or false")
		}
		return s.SetPaused(paused)
	}
	def, ok := definitionFor(key)
	if !ok {
		return errInvalid(key, "unknown setting")
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return errInvalid(key, "must be a whole number")
	}
	if n < def.Min || n > def.Max {
		return errInvalid(key, "must be between "+strconv.FormatInt(def.Min, 10)+" and "+strconv.FormatInt(def.Max, 10))
	}
	return s.db.SetSetting(key, value)
}

func definitionFor(key string) (Definition, bool) {
	for _, d := range Definitions {
		if d.Key == key {
			return d, true
		}
	}
	return Definition{}, false
}

func mustParse(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func (s *Store) boolValue(key string, fallback bool) bool {
	raw, ok, err := s.db.GetSetting(key)
	if err != nil {
		slog.Error("settings: reading", "key", key, "error", err)
		return fallback
	}
	if !ok {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		slog.Warn("settings: unparseable value, using default", "key", key, "value", raw)
		return fallback
	}
	return v
}

// intValue returns the stored integer, clamped to the key's declared
// bounds. Clamping rather than rejecting matters at read time: a value
// that predates a bounds change shouldn't be able to produce a
// one-second sampling loop hammering a downloader.
func (s *Store) intValue(key string, fallback int64) int64 {
	raw, ok, err := s.db.GetSetting(key)
	if err != nil {
		slog.Error("settings: reading", "key", key, "error", err)
		return fallback
	}
	if !ok {
		return fallback
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		slog.Warn("settings: unparseable value, using default", "key", key, "value", raw)
		return fallback
	}
	if def, found := definitionFor(key); found && (v < def.Min || v > def.Max) {
		slog.Warn("settings: value out of range, clamping", "key", key, "value", v)
		if v < def.Min {
			v = def.Min
		} else {
			v = def.Max
		}
	}
	return v
}

func (s *Store) duration(key string, fallback time.Duration) time.Duration {
	return time.Duration(s.intValue(key, int64(fallback/time.Second))) * time.Second
}

func (s *Store) days(key string, fallbackDays int64) time.Duration {
	return time.Duration(s.intValue(key, fallbackDays)) * 24 * time.Hour
}
