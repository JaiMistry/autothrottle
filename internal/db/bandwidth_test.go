package db

import (
	"testing"
	"time"
)

func addTestDownloader(t *testing.T, d *DB, name string) int64 {
	t.Helper()
	id, err := d.CreateDownloader(Downloader{Name: name, Type: "qbittorrent", URL: "http://qb.invalid:8080", Enabled: true})
	if err != nil {
		t.Fatalf("creating downloader: %v", err)
	}
	return id
}

func TestRollUpBandwidthAggregatesAndClearsRawSamples(t *testing.T) {
	d := newTestDB(t)
	id := addTestDownloader(t, d, "qb")

	// Three samples inside one hour, plus one recent sample that must
	// survive because it's newer than the cutoff.
	hour := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	for i, rate := range []int64{100, 200, 300} {
		if err := d.InsertBandwidthSample(BandwidthSample{
			SampledAt:           FormatTime(hour.Add(time.Duration(i) * time.Minute)),
			DownloaderID:        id,
			DownloadBytesPerSec: rate,
			UploadBytesPerSec:   rate / 2,
			Throttled:           i == 2,
			DownloadLimitKBps:   500,
		}); err != nil {
			t.Fatalf("inserting sample: %v", err)
		}
	}
	recent := time.Now()
	if err := d.InsertBandwidthSample(BandwidthSample{
		SampledAt: FormatTime(recent), DownloaderID: id, DownloadBytesPerSec: 999,
	}); err != nil {
		t.Fatalf("inserting recent sample: %v", err)
	}

	folded, err := d.RollUpBandwidth(recent.Add(-time.Hour))
	if err != nil {
		t.Fatalf("rolling up: %v", err)
	}
	if folded != 3 {
		t.Fatalf("folded = %d, want 3", folded)
	}

	rollups, err := d.ListBandwidthRollups(0, hour.Add(-time.Hour), hour.Add(time.Hour))
	if err != nil {
		t.Fatalf("listing rollups: %v", err)
	}
	if len(rollups) != 1 {
		t.Fatalf("got %d rollups, want 1", len(rollups))
	}
	got := rollups[0]
	if got.SampleCount != 3 {
		t.Errorf("SampleCount = %d, want 3", got.SampleCount)
	}
	if got.AvgDownloadBytesPerSec != 200 {
		t.Errorf("AvgDownloadBytesPerSec = %d, want 200", got.AvgDownloadBytesPerSec)
	}
	if got.MaxDownloadBytesPerSec != 300 {
		t.Errorf("MaxDownloadBytesPerSec = %d, want 300", got.MaxDownloadBytesPerSec)
	}
	if got.ThrottledSampleCount != 1 {
		t.Errorf("ThrottledSampleCount = %d, want 1", got.ThrottledSampleCount)
	}

	// The raw rows behind the rollup are gone; the recent one isn't.
	remaining, err := d.ListBandwidthSamples(0, time.Time{}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("listing samples: %v", err)
	}
	if len(remaining) != 1 || remaining[0].DownloadBytesPerSec != 999 {
		t.Fatalf("remaining samples = %+v, want only the recent one", remaining)
	}
}

// A second rollup pass over an hour that already has a bucket must
// merge into it rather than overwriting or double-counting — the case
// that happens whenever samples for one hour get folded across two
// maintenance runs.
func TestRollUpBandwidthMergesIntoExistingHour(t *testing.T) {
	d := newTestDB(t)
	id := addTestDownloader(t, d, "qb")
	hour := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	insert := func(rate int64, minute int) {
		t.Helper()
		if err := d.InsertBandwidthSample(BandwidthSample{
			SampledAt: FormatTime(hour.Add(time.Duration(minute) * time.Minute)), DownloaderID: id,
			DownloadBytesPerSec: rate, Throttled: true,
		}); err != nil {
			t.Fatalf("inserting sample: %v", err)
		}
	}

	insert(100, 0)
	insert(300, 1)
	if _, err := d.RollUpBandwidth(hour.Add(30 * time.Minute)); err != nil {
		t.Fatalf("first rollup: %v", err)
	}

	insert(500, 40)
	insert(700, 41)
	if _, err := d.RollUpBandwidth(hour.Add(time.Hour)); err != nil {
		t.Fatalf("second rollup: %v", err)
	}

	rollups, err := d.ListBandwidthRollups(id, hour, hour)
	if err != nil {
		t.Fatalf("listing rollups: %v", err)
	}
	if len(rollups) != 1 {
		t.Fatalf("got %d rollups, want 1 merged bucket", len(rollups))
	}
	got := rollups[0]
	if got.SampleCount != 4 {
		t.Errorf("SampleCount = %d, want 4", got.SampleCount)
	}
	if got.AvgDownloadBytesPerSec != 400 { // (200*2 + 600*2) / 4
		t.Errorf("AvgDownloadBytesPerSec = %d, want 400", got.AvgDownloadBytesPerSec)
	}
	if got.MaxDownloadBytesPerSec != 700 {
		t.Errorf("MaxDownloadBytesPerSec = %d, want 700", got.MaxDownloadBytesPerSec)
	}
	if got.ThrottledSampleCount != 4 {
		t.Errorf("ThrottledSampleCount = %d, want 4", got.ThrottledSampleCount)
	}
}

func TestPruneBandwidthRollups(t *testing.T) {
	d := newTestDB(t)
	id := addTestDownloader(t, d, "qb")

	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{old, recent} {
		if err := d.InsertBandwidthSample(BandwidthSample{SampledAt: FormatTime(at), DownloaderID: id, DownloadBytesPerSec: 1}); err != nil {
			t.Fatalf("inserting sample: %v", err)
		}
	}
	if _, err := d.RollUpBandwidth(recent.Add(time.Hour)); err != nil {
		t.Fatalf("rolling up: %v", err)
	}

	deleted, err := d.PruneBandwidthRollups(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("pruning: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}

	rollups, err := d.ListBandwidthRollups(0, old, recent)
	if err != nil {
		t.Fatalf("listing rollups: %v", err)
	}
	if len(rollups) != 1 || rollups[0].Hour != FormatTime(recent) {
		t.Fatalf("remaining rollups = %+v, want only the recent hour", rollups)
	}
}
