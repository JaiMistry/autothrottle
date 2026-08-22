package db

import (
	"testing"
	"time"
)

func addSample(t *testing.T, d *DB, downloaderID int64, at time.Time, down, up int64, throttled bool, limitKBps int64) {
	t.Helper()
	if err := d.InsertBandwidthSample(BandwidthSample{
		SampledAt:           FormatTime(at),
		DownloaderID:        downloaderID,
		DownloadBytesPerSec: down,
		UploadBytesPerSec:   up,
		Throttled:           throttled,
		DownloadLimitKBps:   limitKBps,
		UploadLimitKBps:     limitKBps / 2,
	}); err != nil {
		t.Fatalf("inserting sample: %v", err)
	}
}

func TestBandwidthSeriesBucketsRawSamples(t *testing.T) {
	d := newTestDB(t)
	id := addTestDownloader(t, d, "qb")
	base := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	// Six samples a minute apart, bucketed into three-minute buckets.
	for i, rate := range []int64{100, 200, 300, 400, 500, 600} {
		addSample(t, d, id, base.Add(time.Duration(i)*time.Minute), rate, rate/2, false, 0)
	}

	points, err := d.BandwidthSeries(id, base.Add(-time.Hour), base.Add(time.Hour), 3*time.Minute)
	if err != nil {
		t.Fatalf("BandwidthSeries: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("got %d buckets, want 2", len(points))
	}
	if points[0].DownloadBytesPerSec != 200 {
		t.Errorf("first bucket avg = %d, want 200 (avg of 100,200,300)", points[0].DownloadBytesPerSec)
	}
	if points[1].DownloadBytesPerSec != 500 {
		t.Errorf("second bucket avg = %d, want 500 (avg of 400,500,600)", points[1].DownloadBytesPerSec)
	}
	if points[0].SampleCount != 3 {
		t.Errorf("sample count = %d, want 3", points[0].SampleCount)
	}
	if points[0].Resolution != ResolutionRaw {
		t.Errorf("resolution = %q, want raw", points[0].Resolution)
	}
	if !points[0].At.Before(points[1].At) {
		t.Error("points must be ordered oldest first")
	}
}

// A bucket is throttled if any sample in it was, and the limit shown is
// the one in force at the end of the bucket — that is what a step line
// should draw.
func TestBandwidthSeriesReportsThrottleAndTrailingLimit(t *testing.T) {
	d := newTestDB(t)
	id := addTestDownloader(t, d, "qb")
	base := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	addSample(t, d, id, base, 8000, 0, false, 0)                    // unlimited
	addSample(t, d, id, base.Add(time.Minute), 2000, 0, true, 2000) // throttled mid-bucket
	addSample(t, d, id, base.Add(2*time.Minute), 2000, 0, true, 2000)

	points, err := d.BandwidthSeries(id, base.Add(-time.Hour), base.Add(time.Hour), 5*time.Minute)
	if err != nil {
		t.Fatalf("BandwidthSeries: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("got %d buckets, want 1", len(points))
	}
	if !points[0].Throttled {
		t.Error("a bucket containing any throttled sample must report throttled")
	}
	if points[0].DownloadLimitKBps != 2000 {
		t.Errorf("limit = %d, want 2000 (the limit at the end of the bucket)", points[0].DownloadLimitKBps)
	}
}

// An outage leaves no samples. The series must skip those buckets
// entirely rather than emitting zeroes, so a chart can draw a gap
// instead of inventing throughput that never happened.
func TestBandwidthSeriesLeavesGapsForMissingData(t *testing.T) {
	d := newTestDB(t)
	id := addTestDownloader(t, d, "qb")
	base := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	addSample(t, d, id, base, 100, 0, false, 0)
	// ... nothing for an hour ...
	addSample(t, d, id, base.Add(time.Hour), 100, 0, false, 0)

	points, err := d.BandwidthSeries(id, base.Add(-time.Minute), base.Add(2*time.Hour), time.Minute)
	if err != nil {
		t.Fatalf("BandwidthSeries: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("got %d points, want 2 — empty buckets must not be filled in", len(points))
	}
	if gap := points[1].At.Sub(points[0].At); gap != time.Hour {
		t.Errorf("gap between points = %s, want 1h", gap)
	}
}

// Raw samples expire before rollups do, so a long range is partly one
// and partly the other. The join must not drop or duplicate the hour
// where they meet.
func TestBandwidthSeriesStitchesRollupsAndRawSamples(t *testing.T) {
	d := newTestDB(t)
	id := addTestDownloader(t, d, "qb")
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	// Two old hours that have already been rolled up and had their raw
	// samples deleted.
	for h := 0; h < 2; h++ {
		hour := base.Add(time.Duration(h) * time.Hour)
		addSample(t, d, id, hour.Add(time.Minute), 1000, 500, false, 0)
		addSample(t, d, id, hour.Add(2*time.Minute), 3000, 1500, true, 2000)
	}
	if _, err := d.RollUpBandwidth(base.Add(2 * time.Hour)); err != nil {
		t.Fatalf("RollUpBandwidth: %v", err)
	}

	// A recent hour still at full resolution.
	recent := base.Add(2 * time.Hour)
	addSample(t, d, id, recent.Add(time.Minute), 5000, 2500, false, 0)

	points, err := d.BandwidthSeries(id, base.Add(-time.Hour), recent.Add(time.Hour), time.Minute)
	if err != nil {
		t.Fatalf("BandwidthSeries: %v", err)
	}
	if len(points) != 3 {
		t.Fatalf("got %d points, want 3 (two rolled-up hours plus one raw sample)", len(points))
	}
	if points[0].Resolution != ResolutionHour || points[1].Resolution != ResolutionHour {
		t.Errorf("older points should come from rollups: %q %q", points[0].Resolution, points[1].Resolution)
	}
	if points[2].Resolution != ResolutionRaw {
		t.Errorf("newest point should come from raw samples, got %q", points[2].Resolution)
	}
	if points[0].DownloadBytesPerSec != 2000 {
		t.Errorf("rollup avg = %d, want 2000 (avg of 1000 and 3000)", points[0].DownloadBytesPerSec)
	}
	if !points[0].Throttled {
		t.Error("a rolled-up hour with throttled samples must report throttled")
	}
	// Rollups carry no limit data; a UI needs to be able to tell.
	if points[0].DownloadLimitKBps != 0 {
		t.Errorf("rollups have no limit data, got %d", points[0].DownloadLimitKBps)
	}
	for i := 1; i < len(points); i++ {
		if !points[i-1].At.Before(points[i].At) {
			t.Fatalf("points out of order at %d: %s then %s", i, points[i-1].At, points[i].At)
		}
	}
}

func TestBandwidthSummaryTotalsAndThrottleShare(t *testing.T) {
	d := newTestDB(t)
	id := addTestDownloader(t, d, "qb")
	base := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	const interval = time.Minute

	// Four samples: two throttled, two not.
	addSample(t, d, id, base, 1000, 100, false, 0)
	addSample(t, d, id, base.Add(interval), 1000, 100, false, 0)
	addSample(t, d, id, base.Add(2*interval), 500, 50, true, 500)
	addSample(t, d, id, base.Add(3*interval), 500, 50, true, 500)

	summary, err := d.BandwidthSummary(id, base.Add(-time.Hour), base.Add(time.Hour), interval)
	if err != nil {
		t.Fatalf("BandwidthSummary: %v", err)
	}
	if summary.SampleCount != 4 {
		t.Errorf("sample count = %d, want 4", summary.SampleCount)
	}
	// (1000+1000+500+500) bytes/sec × 60s each
	if want := int64(3000 * 60); summary.DownloadBytes != want {
		t.Errorf("download bytes = %d, want %d", summary.DownloadBytes, want)
	}
	if summary.ThrottledSamples != 2 {
		t.Errorf("throttled samples = %d, want 2", summary.ThrottledSamples)
	}
	if summary.ThrottledFraction != 0.5 {
		t.Errorf("throttled fraction = %v, want 0.5", summary.ThrottledFraction)
	}
	if summary.ThrottledDuration != 2*interval {
		t.Errorf("throttled duration = %s, want %s", summary.ThrottledDuration, 2*interval)
	}
	if summary.ObservedDuration != 4*interval {
		t.Errorf("observed duration = %s, want %s", summary.ObservedDuration, 4*interval)
	}
}

func TestBandwidthSummaryOnEmptyWindow(t *testing.T) {
	d := newTestDB(t)
	id := addTestDownloader(t, d, "qb")
	now := time.Now()

	summary, err := d.BandwidthSummary(id, now.Add(-time.Hour), now, time.Minute)
	if err != nil {
		t.Fatalf("a window with no data must not be an error: %v", err)
	}
	if summary.SampleCount != 0 || summary.DownloadBytes != 0 || summary.ThrottledFraction != 0 {
		t.Errorf("expected a zeroed summary, got %+v", summary)
	}
}

func TestThrottlePeriodsFindsRuns(t *testing.T) {
	d := newTestDB(t)
	id := addTestDownloader(t, d, "qb")
	base := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	const interval = time.Minute

	// on, on, off, on, on, on  → two periods
	states := []bool{true, true, false, true, true, true}
	for i, throttled := range states {
		addSample(t, d, id, base.Add(time.Duration(i)*interval), 100, 10, throttled, 0)
	}

	periods, err := d.ThrottlePeriods(id, base.Add(-time.Hour), base.Add(time.Hour), interval)
	if err != nil {
		t.Fatalf("ThrottlePeriods: %v", err)
	}
	if len(periods) != 2 {
		t.Fatalf("got %d periods, want 2: %+v", len(periods), periods)
	}
	if periods[0].Duration() != 2*interval {
		t.Errorf("first period = %s, want %s", periods[0].Duration(), 2*interval)
	}
	if periods[1].Duration() != 3*interval {
		t.Errorf("second period = %s, want %s", periods[1].Duration(), 3*interval)
	}
}

// Throttling that spans an outage is two periods, not one: the app was
// not running to hold the limit in place, so claiming an unbroken
// stretch would be a claim it cannot support.
func TestThrottlePeriodsBreakAcrossDataGaps(t *testing.T) {
	d := newTestDB(t)
	id := addTestDownloader(t, d, "qb")
	base := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	const interval = time.Minute

	addSample(t, d, id, base, 100, 10, true, 0)
	addSample(t, d, id, base.Add(interval), 100, 10, true, 0)
	// ... app down for an hour, still throttled when it returns ...
	addSample(t, d, id, base.Add(time.Hour), 100, 10, true, 0)

	periods, err := d.ThrottlePeriods(id, base.Add(-time.Hour), base.Add(2*time.Hour), interval)
	if err != nil {
		t.Fatalf("ThrottlePeriods: %v", err)
	}
	if len(periods) != 2 {
		t.Fatalf("got %d periods, want 2 (the gap must break the run): %+v", len(periods), periods)
	}
}

func TestThrottlePeriodsNoneWhenNeverThrottled(t *testing.T) {
	d := newTestDB(t)
	id := addTestDownloader(t, d, "qb")
	base := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		addSample(t, d, id, base.Add(time.Duration(i)*time.Minute), 100, 10, false, 0)
	}

	periods, err := d.ThrottlePeriods(id, base.Add(-time.Hour), base.Add(time.Hour), time.Minute)
	if err != nil {
		t.Fatalf("ThrottlePeriods: %v", err)
	}
	if len(periods) != 0 {
		t.Errorf("got %d periods, want none: %+v", len(periods), periods)
	}
}

func TestBandwidthSeriesRejectsUnusableBucket(t *testing.T) {
	d := newTestDB(t)
	id := addTestDownloader(t, d, "qb")
	now := time.Now()

	if _, err := d.BandwidthSeries(id, now.Add(-time.Hour), now, 0); err == nil {
		t.Error("a zero bucket must be rejected rather than dividing by zero in SQL")
	}
	if _, err := d.BandwidthSummary(id, now.Add(-time.Hour), now, 0); err == nil {
		t.Error("a zero sample interval must be rejected")
	}
}

// Regression: an hourly rollup carries how many of its samples were
// throttled. Treating a rollup as "throttled throughout" because one
// sample was inflates time-throttled by up to the bucket size — at
// hourly resolution, a ten-minute stream reported as a full hour.
func TestBandwidthSummaryUsesRealThrottledCountsAtHourlyResolution(t *testing.T) {
	d := newTestDB(t)
	id := addTestDownloader(t, d, "qb")
	hour := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	const interval = time.Minute

	// Ten throttled minutes inside an hour of samples.
	for i := 0; i < 60; i++ {
		addSample(t, d, id, hour.Add(time.Duration(i)*interval), 1000, 100, i < 10, 0)
	}
	if _, err := d.RollUpBandwidth(hour.Add(time.Hour)); err != nil {
		t.Fatalf("RollUpBandwidth: %v", err)
	}

	summary, err := d.BandwidthSummary(id, hour.Add(-time.Hour), hour.Add(2*time.Hour), interval)
	if err != nil {
		t.Fatalf("BandwidthSummary: %v", err)
	}
	if summary.ThrottledSamples != 10 {
		t.Errorf("throttled samples = %d, want 10", summary.ThrottledSamples)
	}
	if summary.ThrottledDuration != 10*interval {
		t.Errorf("throttled duration = %s, want %s", summary.ThrottledDuration, 10*interval)
	}
	if got := summary.ThrottledFraction; got < 0.16 || got > 0.17 {
		t.Errorf("throttled fraction = %v, want about 1/6", got)
	}
}

// The same must hold when several samples share a bucket at raw
// resolution.
func TestBandwidthSeriesReportsThrottledCountPerBucket(t *testing.T) {
	d := newTestDB(t)
	id := addTestDownloader(t, d, "qb")
	base := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	addSample(t, d, id, base, 100, 10, true, 500)
	addSample(t, d, id, base.Add(time.Minute), 100, 10, false, 0)
	addSample(t, d, id, base.Add(2*time.Minute), 100, 10, false, 0)

	points, err := d.BandwidthSeries(id, base.Add(-time.Hour), base.Add(time.Hour), 5*time.Minute)
	if err != nil {
		t.Fatalf("BandwidthSeries: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("got %d buckets, want 1", len(points))
	}
	if points[0].SampleCount != 3 || points[0].ThrottledSampleCount != 1 {
		t.Errorf("counts = %d throttled of %d, want 1 of 3",
			points[0].ThrottledSampleCount, points[0].SampleCount)
	}
	if !points[0].Throttled {
		t.Error("a bucket with any throttled sample should still shade as throttled")
	}
}

// The daily view has to read the same whether a day came from raw
// samples or from rollups — that uniformity is the whole point of it.
func TestDailyBandwidthSpansBothRetentionTiers(t *testing.T) {
	d := newTestDB(t)
	id := addTestDownloader(t, d, "qb")
	day1 := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	day2 := day1.Add(24 * time.Hour)
	const interval = time.Minute

	// Day 1: 60 samples, 30 throttled — then rolled up.
	for i := 0; i < 60; i++ {
		addSample(t, d, id, day1.Add(time.Duration(i)*interval), 1000, 100, i < 30, 0)
	}
	if _, err := d.RollUpBandwidth(day1.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	// Day 2: 60 samples, 30 throttled — still raw.
	for i := 0; i < 60; i++ {
		addSample(t, d, id, day2.Add(time.Duration(i)*interval), 1000, 100, i < 30, 0)
	}

	days, err := d.DailyBandwidth(id, day1.Add(-time.Hour), day2.Add(2*time.Hour), interval)
	if err != nil {
		t.Fatalf("DailyBandwidth: %v", err)
	}
	if len(days) != 2 {
		t.Fatalf("got %d days, want 2: %+v", len(days), days)
	}
	for i, day := range days {
		if day.ThrottledDuration != 30*interval {
			t.Errorf("day %d throttled = %s, want %s", i, day.ThrottledDuration, 30*interval)
		}
		if day.ObservedDuration != 60*interval {
			t.Errorf("day %d observed = %s, want %s", i, day.ObservedDuration, 60*interval)
		}
		if want := int64(1000 * 60 * 60); day.DownloadBytes != want {
			t.Errorf("day %d bytes = %d, want %d", i, day.DownloadBytes, want)
		}
		if day.ThrottledFraction != 0.5 {
			t.Errorf("day %d fraction = %v, want 0.5", i, day.ThrottledFraction)
		}
	}
	if !days[0].Day.Before(days[1].Day) {
		t.Error("days must be ordered oldest first")
	}
}

func TestDailyBandwidthSkipsDaysWithNoData(t *testing.T) {
	d := newTestDB(t)
	id := addTestDownloader(t, d, "qb")
	day1 := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	addSample(t, d, id, day1.Add(time.Hour), 1000, 100, false, 0)
	// nothing on day 2
	addSample(t, d, id, day1.Add(48*time.Hour), 1000, 100, false, 0)

	days, err := d.DailyBandwidth(id, day1, day1.Add(72*time.Hour), time.Minute)
	if err != nil {
		t.Fatalf("DailyBandwidth: %v", err)
	}
	if len(days) != 2 {
		t.Fatalf("got %d days, want 2 — a day with no data must not be invented", len(days))
	}
}
