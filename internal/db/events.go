package db

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// EventFilter narrows a history query. Every field is optional; the
// zero value means "everything, newest first, unpaged".
type EventFilter struct {
	Levels        []string // any of info, warn, error; empty means all
	MediaServerID int64    // 0 means any
	DownloaderID  int64    // 0 means any
	Since, Until  time.Time
	Search        string // case-insensitive substring of the message
	Limit, Offset int
}

// TimeFormat is the textual timestamp format every table in this
// schema stores. Times are UTC and lexicographically sortable, so
// range comparisons work as plain string comparisons.
const TimeFormat = "2006-01-02T15:04:05.000Z"

// FormatTime renders t in the schema's storage format.
func FormatTime(t time.Time) string { return t.UTC().Format(TimeFormat) }

// where builds the shared WHERE clause and its arguments, so the list
// and count queries can never drift apart in what they match.
func (f EventFilter) where() (string, []any) {
	var clauses []string
	var args []any

	if len(f.Levels) > 0 {
		placeholders := make([]string, len(f.Levels))
		for i, level := range f.Levels {
			placeholders[i] = "?"
			args = append(args, level)
		}
		clauses = append(clauses, "level IN ("+strings.Join(placeholders, ", ")+")")
	}
	if f.MediaServerID > 0 {
		clauses = append(clauses, "media_server_id = ?")
		args = append(args, f.MediaServerID)
	}
	if f.DownloaderID > 0 {
		clauses = append(clauses, "downloader_id = ?")
		args = append(args, f.DownloaderID)
	}
	if !f.Since.IsZero() {
		clauses = append(clauses, "occurred_at >= ?")
		args = append(args, FormatTime(f.Since))
	}
	if !f.Until.IsZero() {
		clauses = append(clauses, "occurred_at <= ?")
		args = append(args, FormatTime(f.Until))
	}
	if f.Search != "" {
		// LIKE is case-insensitive for ASCII in SQLite by default,
		// which is what a message search here needs.
		clauses = append(clauses, `message LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(f.Search)+"%")
	}

	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// escapeLike neutralises LIKE wildcards in user-supplied search text,
// so searching for "100%" doesn't match everything.
func escapeLike(s string) string {
	r := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")
	return r.Replace(s)
}

// ListEventsFiltered returns matching events, newest first.
func (d *DB) ListEventsFiltered(f EventFilter) ([]Event, error) {
	where, args := f.where()
	query := `SELECT id, occurred_at, level, message, mapping_id, media_server_id, downloader_id
	          FROM events` + where + ` ORDER BY occurred_at DESC, id DESC`
	if f.Limit > 0 {
		query += " LIMIT " + strconv.Itoa(f.Limit)
		if f.Offset > 0 {
			query += " OFFSET " + strconv.Itoa(f.Offset)
		}
	}

	rows, err := d.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.OccurredAt, &e.Level, &e.Message, &e.MappingID, &e.MediaServerID, &e.DownloaderID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountEvents returns how many events match, ignoring limit and offset
// — the total a paginated view needs to show.
func (d *DB) CountEvents(f EventFilter) (int, error) {
	where, args := f.where()
	var n int
	err := d.conn.QueryRow(`SELECT COUNT(*) FROM events`+where, args...).Scan(&n)
	return n, err
}

// ListEvents returns the most recent events, newest first, capped at
// limit. Shorthand for the common unfiltered case.
func (d *DB) ListEvents(limit int) ([]Event, error) {
	return d.ListEventsFiltered(EventFilter{Limit: limit})
}

// PruneEvents deletes events older than before, returning how many
// rows went. Retention is enforced here rather than by a row cap: "the
// last 90 days" stays meaningful whether the app logged ten events or
// ten thousand.
func (d *DB) PruneEvents(before time.Time) (int64, error) {
	res, err := d.conn.Exec(`DELETE FROM events WHERE occurred_at < ?`, FormatTime(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountEventsByLevel returns how many events match a filter at each
// level, for the Logs page's level tabs — where the count is the
// information, not decoration. The filter's own Levels field is
// ignored: the point is to report every level under the same search
// and time constraints.
func (d *DB) CountEventsByLevel(f EventFilter) (map[string]int, error) {
	f.Levels = nil
	f.Limit, f.Offset = 0, 0
	where, args := f.where()

	rows, err := d.conn.Query(`SELECT level, COUNT(*) FROM events`+where+` GROUP BY level`, args...)
	if err != nil {
		return nil, fmt.Errorf("counting events by level: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]int{"info": 0, "warn": 0, "error": 0}
	for rows.Next() {
		var level string
		var n int
		if err := rows.Scan(&level, &n); err != nil {
			return nil, fmt.Errorf("scanning level count: %w", err)
		}
		out[level] = n
	}
	return out, rows.Err()
}
