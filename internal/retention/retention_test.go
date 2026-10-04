package retention

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/db"
)

// One pass has to do all three jobs. They're independent by design, so
// the risk worth covering is a pass that quietly stops after the first.
func TestRunOnceRollsUpAndPrunes(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	defer func() { _ = database.Close() }()

	dlID, err := database.CreateDownloader(db.Downloader{
		Name: "qbit", Type: "qbittorrent", URL: "http://qbit.invalid", Enabled: true,
	})
	if err != nil {
		t.Fatalf("creating downloader: %v", err)
	}

	now := time.Now()
	// Old enough to be rolled up, recent enough for the rollup to survive.
	if err := database.InsertBandwidthSample(db.BandwidthSample{
		SampledAt: db.FormatTime(now.Add(-10 * 24 * time.Hour)), DownloaderID: dlID, DownloadBytesPerSec: 100,
	}); err != nil {
		t.Fatalf("inserting sample: %v", err)
	}
	// Recent: must stay raw.
	if err := database.InsertBandwidthSample(db.BandwidthSample{
		DownloaderID: dlID, DownloadBytesPerSec: 200,
	}); err != nil {
		t.Fatalf("inserting sample: %v", err)
	}
	if err := database.InsertEvent(db.Event{
		OccurredAt: db.FormatTime(now.Add(-100 * 24 * time.Hour)), Level: "info", Message: "ancient",
	}); err != nil {
		t.Fatalf("inserting event: %v", err)
	}
	if err := database.InsertEvent(db.Event{Level: "info", Message: "fresh"}); err != nil {
		t.Fatalf("inserting event: %v", err)
	}

	New(database).RunOnce()

	samples, err := database.ListBandwidthSamples(dlID, now.Add(-365*24*time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("listing samples: %v", err)
	}
	if len(samples) != 1 || samples[0].DownloadBytesPerSec != 200 {
		t.Errorf("remaining samples = %+v, want only the recent one", samples)
	}

	rollups, err := database.ListBandwidthRollups(dlID, now.Add(-365*24*time.Hour), now)
	if err != nil {
		t.Fatalf("listing rollups: %v", err)
	}
	if len(rollups) != 1 || rollups[0].AvgDownloadBytesPerSec != 100 {
		t.Errorf("rollups = %+v, want the folded old sample", rollups)
	}

	events, err := database.ListEvents(10)
	if err != nil {
		t.Fatalf("listing events: %v", err)
	}
	if len(events) != 1 || events[0].Message != "fresh" {
		t.Errorf("remaining events = %+v, want only the fresh one", events)
	}
}

// Rollups past their own retention window go, even though the raw
// samples behind them were already deleted long before.
func TestRunOnceDropsExpiredRollups(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	defer func() { _ = database.Close() }()

	dlID, err := database.CreateDownloader(db.Downloader{
		Name: "qbit", Type: "qbittorrent", URL: "http://qbit.invalid", Enabled: true,
	})
	if err != nil {
		t.Fatalf("creating downloader: %v", err)
	}

	now := time.Now()
	if err := database.InsertBandwidthSample(db.BandwidthSample{
		SampledAt: db.FormatTime(now.Add(-100 * 24 * time.Hour)), DownloaderID: dlID, DownloadBytesPerSec: 100,
	}); err != nil {
		t.Fatalf("inserting sample: %v", err)
	}

	runner := New(database)
	runner.RunOnce() // folds it into a rollup, then drops that rollup as expired

	rollups, err := database.ListBandwidthRollups(dlID, now.Add(-365*24*time.Hour), now)
	if err != nil {
		t.Fatalf("listing rollups: %v", err)
	}
	if len(rollups) != 0 {
		t.Errorf("rollups = %+v, want none past the retention window", rollups)
	}
}
