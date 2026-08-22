package db

import (
	"database/sql"
	"fmt"
	"time"
)

// Resolution says where a series point came from, because the two
// sources do not carry the same detail: raw samples know the speed
// limit that was in force, hourly rollups do not. A UI showing a
// limit line has to know where it stops being available.
type Resolution string

const (
	ResolutionRaw  Resolution = "raw"
	ResolutionHour Resolution = "hour"
)

// BandwidthPoint is one time bucket of a downloader's throughput.
type BandwidthPoint struct {
	At                  time.Time
	DownloadBytesPerSec int64
	UploadBytesPerSec   int64
	// Throttled reports whether throttling was active at any point in
	// the bucket — what a chart shades. It is deliberately not what
	// totals are derived from: see ThrottledSampleCount.
	Throttled bool
	// ThrottledSampleCount is how many of the bucket's observations
	// were throttled. Totals must use this rather than treating a
	// throttled bucket as throttled throughout, which at hourly
	// resolution would report a ten-minute stream as a full hour.
	ThrottledSampleCount int64
	// DownloadLimitKBps and UploadLimitKBps are the limits in force at
	// the end of the bucket. Zero means unlimited. Only meaningful when
	// Resolution is ResolutionRaw.
	DownloadLimitKBps int64
	UploadLimitKBps   int64
	// SampleCount is how many observations the bucket aggregates. A
	// low count against a long bucket means the app was not running
	// for part of it.
	SampleCount int64
	Resolution  Resolution
}

// BandwidthSeries returns a downloader's throughput over [since,
// until], bucketed to the requested width.
//
// It stitches the two retention tiers. Raw samples are kept for days
// and rollups for months, so a long range is partly one and partly the
// other: rollups cover everything older than the oldest surviving raw
// sample, raw covers the rest. Points are ordered oldest first.
//
// Buckets with no data produce no point rather than a zero. A gap in
// the series means the app was not running, and a chart must render it
// as a gap — interpolating across it would draw throughput that never
// happened.
func (d *DB) BandwidthSeries(downloaderID int64, since, until time.Time, bucket time.Duration) ([]BandwidthPoint, error) {
	if bucket < time.Second {
		return nil, fmt.Errorf("bucket must be at least one second, got %s", bucket)
	}

	oldestRaw, hasRaw, err := d.oldestRawSample(downloaderID)
	if err != nil {
		return nil, err
	}

	// Where raw data begins is where rollups stop being needed.
	rollupUntil := until
	rawSince := since
	switch {
	case !hasRaw:
		rawSince = until // no raw data at all; rollups cover the range
	case oldestRaw.After(since):
		rollupUntil = oldestRaw
		rawSince = oldestRaw
	default:
		rollupUntil = since // raw covers the whole range
	}

	var points []BandwidthPoint
	if rollupUntil.After(since) {
		older, err := d.rollupPoints(downloaderID, since, rollupUntil)
		if err != nil {
			return nil, err
		}
		points = append(points, older...)
	}
	if !rawSince.After(until) {
		recent, err := d.rawPoints(downloaderID, rawSince, until, bucket)
		if err != nil {
			return nil, err
		}
		points = append(points, recent...)
	}
	return points, nil
}

func (d *DB) oldestRawSample(downloaderID int64) (time.Time, bool, error) {
	var oldest sql.NullString
	err := d.conn.QueryRow(
		`SELECT MIN(sampled_at) FROM bandwidth_samples WHERE downloader_id = ?`,
		downloaderID,
	).Scan(&oldest)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("finding oldest sample: %w", err)
	}
	if !oldest.Valid || oldest.String == "" {
		return time.Time{}, false, nil
	}
	parsed, err := time.Parse(TimeFormat, oldest.String)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parsing oldest sample time: %w", err)
	}
	return parsed, true, nil
}

// rawPoints aggregates full-resolution samples into fixed-width
// buckets.
//
// The bare download_limit_kbps and upload_limit_kbps columns rely on
// SQLite's documented behaviour for bare columns in an aggregate
// query: when the query contains exactly one min() or max(), bare
// columns are taken from the row that matched it. Here that is
// max(sampled_at), so the limits are the ones in force at the end of
// the bucket — which is what a step line should draw. This is why
// throttling is counted with SUM rather than MAX: a second max()
// would make which row supplies the bare columns ambiguous.
func (d *DB) rawPoints(downloaderID int64, since, until time.Time, bucket time.Duration) ([]BandwidthPoint, error) {
	bucketSeconds := int64(bucket / time.Second)

	rows, err := d.conn.Query(
		`SELECT
		     (CAST(strftime('%s', sampled_at) AS INTEGER) / ?) * ? AS bucket_start,
		     AVG(download_bytes_per_sec),
		     AVG(upload_bytes_per_sec),
		     SUM(throttled),
		     COUNT(*),
		     max(sampled_at),
		     download_limit_kbps,
		     upload_limit_kbps
		 FROM bandwidth_samples
		 WHERE downloader_id = ? AND sampled_at >= ? AND sampled_at <= ?
		 GROUP BY bucket_start
		 ORDER BY bucket_start`,
		bucketSeconds, bucketSeconds, downloaderID, FormatTime(since), FormatTime(until),
	)
	if err != nil {
		return nil, fmt.Errorf("reading bandwidth samples: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []BandwidthPoint
	for rows.Next() {
		var (
			bucketStart    int64
			avgDown, avgUp float64
			lastSampledAt  string
			p              BandwidthPoint
		)
		if err := rows.Scan(&bucketStart, &avgDown, &avgUp, &p.ThrottledSampleCount,
			&p.SampleCount, &lastSampledAt, &p.DownloadLimitKBps, &p.UploadLimitKBps); err != nil {
			return nil, fmt.Errorf("scanning bandwidth sample: %w", err)
		}
		p.At = time.Unix(bucketStart, 0).UTC()
		p.DownloadBytesPerSec = int64(avgDown)
		p.UploadBytesPerSec = int64(avgUp)
		p.Throttled = p.ThrottledSampleCount > 0
		p.Resolution = ResolutionRaw
		out = append(out, p)
	}
	return out, rows.Err()
}

// rollupPoints reads hourly aggregates. They carry no limit
// information — the rollup table does not store it — so those fields
// stay zero and Resolution marks why.
func (d *DB) rollupPoints(downloaderID int64, since, until time.Time) ([]BandwidthPoint, error) {
	rows, err := d.conn.Query(
		`SELECT hour, avg_download_bytes_per_sec, avg_upload_bytes_per_sec,
		        throttled_sample_count, sample_count
		 FROM bandwidth_rollups_hourly
		 WHERE downloader_id = ? AND hour >= ? AND hour < ?
		 ORDER BY hour`,
		downloaderID, FormatTime(since), FormatTime(until),
	)
	if err != nil {
		return nil, fmt.Errorf("reading bandwidth rollups: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []BandwidthPoint
	for rows.Next() {
		var (
			hour string
			p    BandwidthPoint
		)
		if err := rows.Scan(&hour, &p.DownloadBytesPerSec, &p.UploadBytesPerSec,
			&p.ThrottledSampleCount, &p.SampleCount); err != nil {
			return nil, fmt.Errorf("scanning bandwidth rollup: %w", err)
		}
		parsed, err := time.Parse(TimeFormat, hour)
		if err != nil {
			return nil, fmt.Errorf("parsing rollup hour: %w", err)
		}
		p.At = parsed
		p.Throttled = p.ThrottledSampleCount > 0
		p.Resolution = ResolutionHour
		out = append(out, p)
	}
	return out, rows.Err()
}

// BandwidthSummary is the stat-tile view of a window.
type BandwidthSummary struct {
	// DownloadBytes and UploadBytes are totals for the window,
	// estimated as rate × sampling interval per observation. They are
	// not counter readings — the download clients report instantaneous
	// rates, not cumulative totals — so they are approximate, and more
	// so if the sampling interval was changed during the window.
	DownloadBytes int64
	UploadBytes   int64

	SampleCount      int64
	ThrottledSamples int64
	// ThrottledDuration is likewise sample-derived, so it is accurate
	// to within one sampling interval.
	ThrottledDuration time.Duration
	// ThrottledFraction is the share of observed time spent throttled,
	// between 0 and 1. It is a share of time actually observed, not of
	// the requested window: if the app was down for half the window,
	// the denominator is the half it saw.
	ThrottledFraction float64
	ObservedDuration  time.Duration
}

// BandwidthSummary totals a downloader's window from both retention
// tiers. sampleInterval is what each observation represents; pass the
// configured sampling interval.
func (d *DB) BandwidthSummary(downloaderID int64, since, until time.Time, sampleInterval time.Duration) (BandwidthSummary, error) {
	var summary BandwidthSummary
	if sampleInterval <= 0 {
		return summary, fmt.Errorf("sample interval must be positive, got %s", sampleInterval)
	}

	points, err := d.BandwidthSeries(downloaderID, since, until, sampleInterval)
	if err != nil {
		return summary, err
	}

	seconds := int64(sampleInterval / time.Second)
	for _, p := range points {
		summary.DownloadBytes += p.DownloadBytesPerSec * seconds * p.SampleCount
		summary.UploadBytes += p.UploadBytesPerSec * seconds * p.SampleCount
		summary.SampleCount += p.SampleCount
		summary.ThrottledSamples += p.ThrottledSampleCount
	}

	summary.ObservedDuration = time.Duration(summary.SampleCount) * sampleInterval
	summary.ThrottledDuration = time.Duration(summary.ThrottledSamples) * sampleInterval
	if summary.SampleCount > 0 {
		summary.ThrottledFraction = float64(summary.ThrottledSamples) / float64(summary.SampleCount)
	}
	return summary, nil
}

// ThrottlePeriod is one continuous stretch of throttling.
type ThrottlePeriod struct {
	Start time.Time
	End   time.Time
}

// Duration is how long the period lasted.
func (p ThrottlePeriod) Duration() time.Duration { return p.End.Sub(p.Start) }

// ThrottlePeriods finds continuous runs of throttling in a window, for
// the "4 periods, avg 1h 25m" tile.
//
// Derived from samples rather than from the event log because the event
// log only records transitions the scheduler actually applied: it says
// nothing while a downloader stays throttled, so a run that began
// before the window opened has no event inside it to find. Samples
// describe every moment they cover.
//
// Runs are cut wherever observations stop, so a period spanning an
// outage is reported as two, which is the honest reading — the app
// cannot claim throttling continued while it was not running to apply
// it. sampleInterval sets how large a gap counts as a break.
func (d *DB) ThrottlePeriods(downloaderID int64, since, until time.Time, sampleInterval time.Duration) ([]ThrottlePeriod, error) {
	if sampleInterval <= 0 {
		return nil, fmt.Errorf("sample interval must be positive, got %s", sampleInterval)
	}

	points, err := d.BandwidthSeries(downloaderID, since, until, sampleInterval)
	if err != nil {
		return nil, err
	}

	// A gap wider than this means observations stopped rather than
	// simply moving to the next bucket.
	maxGap := 2 * sampleInterval

	var (
		periods []ThrottlePeriod
		open    *ThrottlePeriod
		prevEnd time.Time
	)
	for _, p := range points {
		pointEnd := p.At.Add(sampleInterval)
		contiguous := open != nil && !p.At.After(prevEnd.Add(maxGap))

		switch {
		case p.Throttled && contiguous:
			open.End = pointEnd
		case p.Throttled:
			if open != nil {
				periods = append(periods, *open)
			}
			open = &ThrottlePeriod{Start: p.At, End: pointEnd}
		case open != nil:
			periods = append(periods, *open)
			open = nil
		}
		prevEnd = pointEnd
	}
	if open != nil {
		periods = append(periods, *open)
	}
	return periods, nil
}

// DailyBandwidth is one calendar day of a downloader's activity.
type DailyBandwidth struct {
	Day           time.Time
	DownloadBytes int64
	UploadBytes   int64
	// ThrottledDuration and ObservedDuration are sample-derived, so a
	// day the app was partly down reports less observed time than 24h
	// — which is the honest reading, and what makes a gap visible at
	// this resolution.
	ThrottledDuration time.Duration
	ObservedDuration  time.Duration
	// ThrottledFraction is throttled over observed, 0 to 1.
	ThrottledFraction float64
}

// DailyBandwidth aggregates a downloader's window into calendar days
// in UTC.
//
// This exists because an instantaneous-rate line stops being the right
// picture past about a day. Over weeks nobody is reading the shape of
// the curve; the question becomes how much moved each day and how much
// of each day was throttled — which a bar answers and a squiggle does
// not.
//
// It also removes the seam. A month-long range is part full-resolution
// samples and part hourly rollups, and drawing both on one rate line
// makes the chart visibly change character partway along, with the
// speed-limit series appearing out of nowhere at the boundary. Rolling
// everything up to a day makes the two tiers indistinguishable,
// because both can produce an honest daily total.
func (d *DB) DailyBandwidth(downloaderID int64, since, until time.Time, sampleInterval time.Duration) ([]DailyBandwidth, error) {
	if sampleInterval <= 0 {
		return nil, fmt.Errorf("sample interval must be positive, got %s", sampleInterval)
	}

	// One hour is the finest bucket both tiers can supply, so asking
	// for it makes the two sources return the same shape.
	points, err := d.BandwidthSeries(downloaderID, since, until, time.Hour)
	if err != nil {
		return nil, err
	}

	seconds := int64(sampleInterval / time.Second)
	byDay := make(map[time.Time]*DailyBandwidth)
	var order []time.Time

	for _, p := range points {
		day := p.At.UTC().Truncate(24 * time.Hour)
		entry, ok := byDay[day]
		if !ok {
			entry = &DailyBandwidth{Day: day}
			byDay[day] = entry
			order = append(order, day)
		}
		entry.DownloadBytes += p.DownloadBytesPerSec * seconds * p.SampleCount
		entry.UploadBytes += p.UploadBytesPerSec * seconds * p.SampleCount
		entry.ObservedDuration += time.Duration(p.SampleCount) * sampleInterval
		entry.ThrottledDuration += time.Duration(p.ThrottledSampleCount) * sampleInterval
	}

	out := make([]DailyBandwidth, 0, len(order))
	for _, day := range order {
		entry := byDay[day]
		if entry.ObservedDuration > 0 {
			entry.ThrottledFraction = float64(entry.ThrottledDuration) / float64(entry.ObservedDuration)
		}
		out = append(out, *entry)
	}
	return out, nil
}
