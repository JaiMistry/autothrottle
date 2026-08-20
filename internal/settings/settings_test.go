package settings

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/db"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return New(database)
}

func TestDefaultsApplyWhenNothingIsStored(t *testing.T) {
	s := newTestStore(t)

	if got := s.SessionReconcileInterval(); got != DefaultSessionReconcileInterval {
		t.Errorf("SessionReconcileInterval = %v, want %v", got, DefaultSessionReconcileInterval)
	}
	if got := s.EventRetention(); got != DefaultEventRetentionDays*24*time.Hour {
		t.Errorf("EventRetention = %v, want %d days", got, DefaultEventRetentionDays)
	}
	if s.Paused() {
		t.Error("Paused = true, want false by default")
	}
}

func TestSetAndRead(t *testing.T) {
	s := newTestStore(t)

	if err := s.Set(KeyBandwidthSampleInterval, "30"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := s.BandwidthSampleInterval(); got != 30*time.Second {
		t.Errorf("BandwidthSampleInterval = %v, want 30s", got)
	}
}

func TestSetRejectsBadValues(t *testing.T) {
	s := newTestStore(t)

	for name, tc := range map[string]struct{ key, value string }{
		"unknown key":  {"nope.not.a.setting", "1"},
		"not a number": {KeyBandwidthSampleInterval, "soon"},
		"below min":    {KeyBandwidthSampleInterval, "1"},
		"above max":    {KeyBandwidthSampleInterval, "999999"},
		"bad bool":     {KeyPaused, "maybe"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := s.Set(tc.key, tc.value); err == nil {
				t.Errorf("Set(%q, %q) = nil, want an error", tc.key, tc.value)
			}
		})
	}
}

// A value written before a bounds change (or straight into the
// database) must not be able to produce a one-second sampling loop
// hammering a downloader.
func TestOutOfRangeStoredValueIsClamped(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	defer func() { _ = database.Close() }()

	if err := database.SetSetting(KeyBandwidthSampleInterval, "1"); err != nil {
		t.Fatalf("writing raw setting: %v", err)
	}

	if got := New(database).BandwidthSampleInterval(); got != 10*time.Second {
		t.Errorf("BandwidthSampleInterval = %v, want it clamped to the 10s minimum", got)
	}
}

func TestUnparseableStoredValueFallsBackToDefault(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	defer func() { _ = database.Close() }()

	if err := database.SetSetting(KeyMaintenanceInterval, "hourly"); err != nil {
		t.Fatalf("writing raw setting: %v", err)
	}

	if got := New(database).MaintenanceInterval(); got != DefaultMaintenanceInterval {
		t.Errorf("MaintenanceInterval = %v, want the default", got)
	}
}

func TestPausedRoundTrips(t *testing.T) {
	s := newTestStore(t)

	if err := s.SetPaused(true); err != nil {
		t.Fatalf("SetPaused: %v", err)
	}
	if !s.Paused() {
		t.Fatal("Paused = false after SetPaused(true)")
	}
	if err := s.SetPaused(false); err != nil {
		t.Fatalf("SetPaused: %v", err)
	}
	if s.Paused() {
		t.Fatal("Paused = true after SetPaused(false)")
	}
}

// All is what a configuration page renders from, so it has to report
// every setting — defaults included, not just what's been overridden.
func TestAllReportsEverySetting(t *testing.T) {
	s := newTestStore(t)
	if err := s.Set(KeyEventRetentionDays, "30"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	all := s.All()
	if len(all) != len(Definitions) {
		t.Fatalf("got %d settings, want %d", len(all), len(Definitions))
	}
	if all[KeyEventRetentionDays] != "30" {
		t.Errorf("overridden value = %q, want 30", all[KeyEventRetentionDays])
	}
	if all[KeyBandwidthSampleInterval] != "60" {
		t.Errorf("default value = %q, want 60", all[KeyBandwidthSampleInterval])
	}
}

// Every definition's declared default must be inside its own bounds —
// a mismatch would mean a fresh install runs on a value the UI would
// reject.
func TestDefinitionDefaultsAreWithinBounds(t *testing.T) {
	for _, def := range Definitions {
		n := mustParse(def.Default)
		if n < def.Min || n > def.Max {
			t.Errorf("%s: default %s outside bounds %d..%d", def.Key, def.Default, def.Min, def.Max)
		}
	}
}
