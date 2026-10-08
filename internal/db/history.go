package db

import (
	"database/sql"
	"fmt"
	"time"
)

// Span is a stretch of time. A zero End is still open.
type Span struct {
	Start, End time.Time
}

// StartRun records that the process has started, closing whatever the
// previous run left open: a throttle period or session that was in
// progress when it stopped ends at that run's last heartbeat, the last
// moment it is known to have been alive. It returns the new run's id.
func (d *DB) StartRun(now time.Time) (int64, error) {
	var lastSeen sql.NullString
	if err := d.conn.QueryRow(`SELECT MAX(last_seen_at) FROM runs`).Scan(&lastSeen); err != nil {
		return 0, fmt.Errorf("reading the last run: %w", err)
	}
	// With no earlier run there is nothing to clamp to, so anything open
	// ends where it began rather than being credited with the downtime.
	if _, err := d.conn.Exec(
		`UPDATE throttle_periods SET ended_at = max(started_at, COALESCE(?, started_at)) WHERE ended_at IS NULL`,
		lastSeen,
	); err != nil {
		return 0, fmt.Errorf("closing dangling throttle periods: %w", err)
	}
	if _, err := d.conn.Exec(
		`UPDATE session_history SET ended_at = max(started_at, COALESCE(?, started_at)) WHERE ended_at IS NULL`,
		lastSeen,
	); err != nil {
		return 0, fmt.Errorf("closing dangling sessions: %w", err)
	}
	result, err := d.conn.Exec(`INSERT INTO runs (started_at, last_seen_at) VALUES (?, ?)`, FormatTime(now), FormatTime(now))
	if err != nil {
		return 0, fmt.Errorf("recording the run: %w", err)
	}
	return result.LastInsertId()
}

// Heartbeat moves a run's last-seen time forward.
func (d *DB) Heartbeat(runID int64, now time.Time) error {
	_, err := d.conn.Exec(`UPDATE runs SET last_seen_at = ? WHERE id = ?`, FormatTime(now), runID)
	return err
}

// OpenThrottlePeriod records that throttling has started.
func (d *DB) OpenThrottlePeriod(at time.Time) error {
	_, err := d.conn.Exec(`INSERT INTO throttle_periods (started_at) VALUES (?)`, FormatTime(at))
	return err
}

// CloseThrottlePeriod ends the period in progress, if there is one.
func (d *DB) CloseThrottlePeriod(at time.Time) error {
	_, err := d.conn.Exec(`UPDATE throttle_periods SET ended_at = ? WHERE ended_at IS NULL`, FormatTime(at))
	return err
}

// ThrottlePeriods returns the periods overlapping [since, until), oldest
// first. The one in progress has a zero End.
func (d *DB) ThrottlePeriods(since, until time.Time) ([]Span, error) {
	return d.spans(
		`SELECT started_at, ended_at FROM throttle_periods
		 WHERE started_at < ? AND (ended_at IS NULL OR ended_at > ?)
		 ORDER BY started_at`, since, until)
}

// Runs returns the spans the process was running that overlap
// [since, until), oldest first, each ending at its last heartbeat.
func (d *DB) Runs(since, until time.Time) ([]Span, error) {
	return d.spans(
		`SELECT started_at, last_seen_at FROM runs
		 WHERE started_at < ? AND last_seen_at > ?
		 ORDER BY started_at`, since, until)
}

func (d *DB) spans(query string, since, until time.Time) ([]Span, error) {
	rows, err := d.conn.Query(query, FormatTime(until), FormatTime(since))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Span
	for rows.Next() {
		var start string
		var end sql.NullString
		if err := rows.Scan(&start, &end); err != nil {
			return nil, err
		}
		var s Span
		if s.Start, err = time.Parse(TimeFormat, start); err != nil {
			return nil, err
		}
		if end.Valid {
			if s.End, err = time.Parse(TimeFormat, end.String); err != nil {
				return nil, err
			}
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// PruneHistory drops runs and finished throttle periods that ended
// before the cutoff.
func (d *DB) PruneHistory(before time.Time) (int64, error) {
	cutoff := FormatTime(before)
	runs, err := d.conn.Exec(`DELETE FROM runs WHERE last_seen_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("pruning runs: %w", err)
	}
	periods, err := d.conn.Exec(`DELETE FROM throttle_periods WHERE ended_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("pruning throttle periods: %w", err)
	}
	a, _ := runs.RowsAffected()
	b, _ := periods.RowsAffected()
	return a + b, nil
}
