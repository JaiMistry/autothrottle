package sampler

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/db"
)

// fakeReporter stands in for a download client. Every test here runs
// against it — never a real qBittorrent.
type fakeReporter struct {
	mu       sync.Mutex
	down, up int64
	err      error
	calls    int
}

func (f *fakeReporter) Rates(context.Context, string, string, string) (int64, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return 0, 0, f.err
	}
	return f.down, f.up, nil
}

// fakeThrottle reports a fixed applied state.
type fakeThrottle struct {
	throttled bool
	down, up  int64
	known     bool
}

func (f fakeThrottle) AppliedLimits(int64) (bool, int64, int64, bool) {
	return f.throttled, f.down, f.up, f.known
}

func newTestSampler(t *testing.T, throttle throttleState) (*Sampler, *db.DB, *fakeReporter) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	reporter := &fakeReporter{down: 4096, up: 512}
	s := New(database, make([]byte, 32), throttle)
	s.reporterFor = func(string) clients.RateReporter { return reporter }
	return s, database, reporter
}

func addDownloader(t *testing.T, database *db.DB, enabled bool) int64 {
	t.Helper()
	id, err := database.CreateDownloader(db.Downloader{
		Name: "qbit", Type: "qbittorrent", URL: "http://qbit.invalid",
		DefaultDownloadLimit: 2000, DefaultUploadLimit: 400,
		BackupDownloadLimit: 500, BackupUploadLimit: 100,
		Enabled: enabled,
	})
	if err != nil {
		t.Fatalf("creating downloader: %v", err)
	}
	return id
}

func TestSampleOnceRecordsRatesWithAppliedLimits(t *testing.T) {
	s, database, _ := newTestSampler(t, fakeThrottle{throttled: true, down: 500, up: 100, known: true})
	id := addDownloader(t, database, true)

	s.SampleOnce(context.Background())

	samples, err := database.ListBandwidthSamples(id, time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("listing samples: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("got %d samples, want 1", len(samples))
	}
	got := samples[0]
	if got.DownloadBytesPerSec != 4096 || got.UploadBytesPerSec != 512 {
		t.Errorf("rates = %d/%d, want 4096/512", got.DownloadBytesPerSec, got.UploadBytesPerSec)
	}
	if !got.Throttled || got.DownloadLimitKBps != 500 || got.UploadLimitKBps != 100 {
		t.Errorf("throttle state = %+v, want the applied throttled limits", got)
	}
}

// Before the scheduler has resolved anything, a downloader is sitting
// at its configured default rate — that's what the sample should say,
// not a zero ceiling.
func TestSampleOnceFallsBackToDefaultLimitsWhenUnresolved(t *testing.T) {
	s, database, _ := newTestSampler(t, fakeThrottle{known: false})
	id := addDownloader(t, database, true)

	s.SampleOnce(context.Background())

	samples, err := database.ListBandwidthSamples(id, time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("listing samples: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("got %d samples, want 1", len(samples))
	}
	if got := samples[0]; got.Throttled || got.DownloadLimitKBps != 2000 || got.UploadLimitKBps != 400 {
		t.Errorf("got %+v, want the downloader's default limits", got)
	}
}

func TestSampleOnceSkipsDisabledDownloaders(t *testing.T) {
	s, database, reporter := newTestSampler(t, fakeThrottle{})
	addDownloader(t, database, false)

	s.SampleOnce(context.Background())

	if reporter.calls != 0 {
		t.Errorf("queried a disabled downloader %d times, want 0", reporter.calls)
	}
}

// An unreachable downloader must not write a sample: a fabricated zero
// would be indistinguishable from a genuinely idle client in the
// analytics that read this table.
func TestSampleOnceRecordsNothingWhenTheClientIsUnreachable(t *testing.T) {
	s, database, reporter := newTestSampler(t, fakeThrottle{known: true})
	id := addDownloader(t, database, true)
	reporter.err = errors.New("connection refused")

	s.SampleOnce(context.Background())

	samples, err := database.ListBandwidthSamples(id, time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("listing samples: %v", err)
	}
	if len(samples) != 0 {
		t.Fatalf("got %d samples, want none recorded for a failed read", len(samples))
	}
}

// A downloader type with no rate reporting is skipped silently, not
// treated as an error — the state Transmission is in until its client
// lands.
func TestSampleOnceSkipsTypesWithoutRateReporting(t *testing.T) {
	s, database, _ := newTestSampler(t, fakeThrottle{})
	id := addDownloader(t, database, true)
	s.reporterFor = func(string) clients.RateReporter { return nil }

	s.SampleOnce(context.Background())

	samples, err := database.ListBandwidthSamples(id, time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("listing samples: %v", err)
	}
	if len(samples) != 0 {
		t.Fatalf("got %d samples, want none", len(samples))
	}
}
