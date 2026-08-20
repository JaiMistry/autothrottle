package db

import (
	"fmt"
	"time"
)

// BandwidthSample is one downloader's observed transfer rates at one
// instant, together with the throttle state and rate ceiling that were
// in force — so analytics can plot actual throughput against the limit
// without re-deriving history from the event log.
type BandwidthSample struct {
	ID                                     int64
	SampledAt                              string
	DownloaderID                           int64
	DownloadBytesPerSec, UploadBytesPerSec int64
	Throttled                              bool
	DownloadLimitKBps, UploadLimitKBps     int64
}

// BandwidthRollup is one downloader's aggregated bandwidth for one
// hour, retained long after the raw samples behind it are gone.
type BandwidthRollup struct {
	Hour                   string
	DownloaderID           int64
	SampleCount            int64
	AvgDownloadBytesPerSec int64
	AvgUploadBytesPerSec   int64
	MaxDownloadBytesPerSec int64
	MaxUploadBytesPerSec   int64
	ThrottledSampleCount   int64
}

// InsertBandwidthSample records one sample. An empty SampledAt means
// "now", which is the normal path; setting it explicitly exists for
// backfill and for tests that need to place a sample in the past.
func (d *DB) InsertBandwidthSample(s BandwidthSample) error {
	if s.SampledAt == "" {
		_, err := d.conn.Exec(
			`INSERT INTO bandwidth_samples
			 (downloader_id, download_bytes_per_sec, upload_bytes_per_sec, throttled, download_limit_kbps, upload_limit_kbps)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			s.DownloaderID, s.DownloadBytesPerSec, s.UploadBytesPerSec, s.Throttled, s.DownloadLimitKBps, s.UploadLimitKBps,
		)
		return err
	}
	_, err := d.conn.Exec(
		`INSERT INTO bandwidth_samples
		 (sampled_at, downloader_id, download_bytes_per_sec, upload_bytes_per_sec, throttled, download_limit_kbps, upload_limit_kbps)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.SampledAt, s.DownloaderID, s.DownloadBytesPerSec, s.UploadBytesPerSec, s.Throttled, s.DownloadLimitKBps, s.UploadLimitKBps,
	)
	return err
}

// ListBandwidthSamples returns raw samples in the given window, oldest
// first (the order a time-series chart plots them in). A downloaderID
// of 0 means every downloader.
func (d *DB) ListBandwidthSamples(downloaderID int64, since, until time.Time) ([]BandwidthSample, error) {
	query := `SELECT id, sampled_at, downloader_id, download_bytes_per_sec, upload_bytes_per_sec,
	                 throttled, download_limit_kbps, upload_limit_kbps
	          FROM bandwidth_samples WHERE sampled_at >= ? AND sampled_at <= ?`
	args := []any{FormatTime(since), FormatTime(until)}
	if downloaderID > 0 {
		query += " AND downloader_id = ?"
		args = append(args, downloaderID)
	}
	query += " ORDER BY sampled_at ASC, id ASC"

	rows, err := d.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []BandwidthSample
	for rows.Next() {
		var s BandwidthSample
		if err := rows.Scan(&s.ID, &s.SampledAt, &s.DownloaderID, &s.DownloadBytesPerSec, &s.UploadBytesPerSec,
			&s.Throttled, &s.DownloadLimitKBps, &s.UploadLimitKBps); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListBandwidthRollups returns hourly aggregates in the given window,
// oldest first. A downloaderID of 0 means every downloader.
func (d *DB) ListBandwidthRollups(downloaderID int64, since, until time.Time) ([]BandwidthRollup, error) {
	query := `SELECT hour, downloader_id, sample_count, avg_download_bytes_per_sec, avg_upload_bytes_per_sec,
	                 max_download_bytes_per_sec, max_upload_bytes_per_sec, throttled_sample_count
	          FROM bandwidth_rollups_hourly WHERE hour >= ? AND hour <= ?`
	args := []any{hourBucket(since), hourBucket(until)}
	if downloaderID > 0 {
		query += " AND downloader_id = ?"
		args = append(args, downloaderID)
	}
	query += " ORDER BY hour ASC"

	rows, err := d.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []BandwidthRollup
	for rows.Next() {
		var r BandwidthRollup
		if err := rows.Scan(&r.Hour, &r.DownloaderID, &r.SampleCount, &r.AvgDownloadBytesPerSec, &r.AvgUploadBytesPerSec,
			&r.MaxDownloadBytesPerSec, &r.MaxUploadBytesPerSec, &r.ThrottledSampleCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RollUpBandwidth folds every raw sample older than before into its
// hourly bucket and deletes the raw rows, returning how many were
// folded.
//
// Aggregation and deletion share one transaction: a crash between them
// would otherwise either lose samples outright or double-count them on
// the next run. The upsert merges into any existing bucket by
// recomputing a count-weighted average, so re-running over an hour
// that was already partially rolled up stays correct.
func (d *DB) RollUpBandwidth(before time.Time) (int64, error) {
	cutoff := FormatTime(before)

	tx, err := d.conn.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	if _, err := tx.Exec(
		`INSERT INTO bandwidth_rollups_hourly AS r
		   (hour, downloader_id, sample_count, avg_download_bytes_per_sec, avg_upload_bytes_per_sec,
		    max_download_bytes_per_sec, max_upload_bytes_per_sec, throttled_sample_count)
		 SELECT substr(sampled_at, 1, 13) || ':00:00.000Z',
		        downloader_id,
		        COUNT(*),
		        CAST(AVG(download_bytes_per_sec) AS INTEGER),
		        CAST(AVG(upload_bytes_per_sec) AS INTEGER),
		        MAX(download_bytes_per_sec),
		        MAX(upload_bytes_per_sec),
		        SUM(throttled)
		 FROM bandwidth_samples
		 WHERE sampled_at < ?
		 GROUP BY 1, 2
		 ON CONFLICT(hour, downloader_id) DO UPDATE SET
		   avg_download_bytes_per_sec =
		     (r.avg_download_bytes_per_sec * r.sample_count + excluded.avg_download_bytes_per_sec * excluded.sample_count)
		     / (r.sample_count + excluded.sample_count),
		   avg_upload_bytes_per_sec =
		     (r.avg_upload_bytes_per_sec * r.sample_count + excluded.avg_upload_bytes_per_sec * excluded.sample_count)
		     / (r.sample_count + excluded.sample_count),
		   max_download_bytes_per_sec = MAX(r.max_download_bytes_per_sec, excluded.max_download_bytes_per_sec),
		   max_upload_bytes_per_sec = MAX(r.max_upload_bytes_per_sec, excluded.max_upload_bytes_per_sec),
		   throttled_sample_count = r.throttled_sample_count + excluded.throttled_sample_count,
		   sample_count = r.sample_count + excluded.sample_count`,
		cutoff,
	); err != nil {
		return 0, fmt.Errorf("rolling up samples: %w", err)
	}

	res, err := tx.Exec(`DELETE FROM bandwidth_samples WHERE sampled_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("deleting rolled-up samples: %w", err)
	}
	folded, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return folded, nil
}

// PruneBandwidthRollups deletes hourly aggregates older than before.
func (d *DB) PruneBandwidthRollups(before time.Time) (int64, error) {
	res, err := d.conn.Exec(`DELETE FROM bandwidth_rollups_hourly WHERE hour < ?`, hourBucket(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// hourBucket renders t as the rollup table's hour key, matching the
// SQL expression RollUpBandwidth groups by.
func hourBucket(t time.Time) string {
	return t.UTC().Format("2006-01-02T15") + ":00:00.000Z"
}
