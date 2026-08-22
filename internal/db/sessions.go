package db

import (
	"database/sql"
	"fmt"
	"time"
)

// SessionRecord is one playback session as it appeared to the app:
// when it started counting toward throttling and when it stopped.
type SessionRecord struct {
	ID            int64
	MediaServerID int64
	SessionKey    string
	StartedAt     string
	// EndedAt is invalid while the session is still playing.
	EndedAt sql.NullString
}

// Ended reports whether the session has finished.
func (s SessionRecord) Ended() bool { return s.EndedAt.Valid && s.EndedAt.String != "" }

// OpenSession records the start of a session, unless one is already
// open for this server and key.
//
// The idempotence matters: the scheduler learns a session is playing
// from two independent inputs — the live event stream and the periodic
// snapshot reconciliation — and they routinely report the same session.
// Without this guard every reconciliation poll would open a duplicate
// row for anything still playing.
func (d *DB) OpenSession(mediaServerID int64, sessionKey string) error {
	var existing int64
	err := d.conn.QueryRow(
		`SELECT id FROM session_history
		 WHERE media_server_id = ? AND session_key = ? AND ended_at IS NULL`,
		mediaServerID, sessionKey,
	).Scan(&existing)
	if err == nil {
		return nil // already open
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("checking for an open session: %w", err)
	}

	_, err = d.conn.Exec(
		`INSERT INTO session_history (media_server_id, session_key) VALUES (?, ?)`,
		mediaServerID, sessionKey,
	)
	if err != nil {
		return fmt.Errorf("opening session: %w", err)
	}
	return nil
}

// CloseSession ends the open session for this server and key, if there
// is one. Closing something already closed is not an error — the same
// duplicate-input reasoning as OpenSession applies in reverse.
func (d *DB) CloseSession(mediaServerID int64, sessionKey string) error {
	_, err := d.conn.Exec(
		`UPDATE session_history
		 SET ended_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE media_server_id = ? AND session_key = ? AND ended_at IS NULL`,
		mediaServerID, sessionKey,
	)
	if err != nil {
		return fmt.Errorf("closing session: %w", err)
	}
	return nil
}

// CloseSessionsForServer ends every open session for one media server.
// Used when a server is disabled, deleted, or its watcher stops: those
// sessions are no longer observable, so leaving them open would let
// them run to the right edge of every chart forever.
func (d *DB) CloseSessionsForServer(mediaServerID int64) error {
	_, err := d.conn.Exec(
		`UPDATE session_history
		 SET ended_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE media_server_id = ? AND ended_at IS NULL`,
		mediaServerID,
	)
	if err != nil {
		return fmt.Errorf("closing sessions for media server: %w", err)
	}
	return nil
}

// CloseDanglingSessions ends sessions left open by a process that
// stopped without closing them, and reports how many it closed.
//
// The honest end time is unknowable — the app was not running to
// observe it — so this uses the last moment the app is known to have
// been alive, which is the newest bandwidth sample. Falling back to
// the session's own start time keeps a dangling session from being
// credited with the entire duration of an outage, which on a home
// server that sat powered off for a week would otherwise dominate
// every chart it appears in.
func (d *DB) CloseDanglingSessions() (int64, error) {
	result, err := d.conn.Exec(
		`UPDATE session_history
		 SET ended_at = max(
		     started_at,
		     COALESCE((SELECT MAX(sampled_at) FROM bandwidth_samples), started_at)
		 )
		 WHERE ended_at IS NULL`,
	)
	if err != nil {
		return 0, fmt.Errorf("closing dangling sessions: %w", err)
	}
	return result.RowsAffected()
}

// mappedServersClause restricts a query to the media servers whose
// playback actually throttles one downloader. A downloaderID of 0
// means every server.
//
// Analytics is presented per downloader, so an unmapped server's
// sessions did not drive that downloader's throttling and must not
// appear alongside its chart. Note this filters on the mapping's
// *current* state: a mapping enabled last week and disabled today
// takes its history with it, because nothing records when a mapping
// changed.
func mappedServersClause(downloaderID int64) (string, []any) {
	if downloaderID == 0 {
		return "", nil
	}
	return ` AND media_server_id IN (
		SELECT media_server_id FROM mappings
		WHERE downloader_id = ? AND enabled = 1
	)`, []any{downloaderID}
}

// ListSessions returns sessions overlapping [since, until], newest
// first. A session counts as overlapping while it is still open, so an
// in-progress session always appears in a window ending now.
//
// downloaderID restricts the result to servers mapped to that
// downloader; 0 returns every server's sessions.
func (d *DB) ListSessions(downloaderID int64, since, until time.Time) ([]SessionRecord, error) {
	clause, args := mappedServersClause(downloaderID)
	params := append([]any{FormatTime(until), FormatTime(since)}, args...)

	rows, err := d.conn.Query(
		`SELECT id, media_server_id, session_key, started_at, ended_at
		 FROM session_history
		 WHERE started_at <= ? AND (ended_at IS NULL OR ended_at >= ?)`+clause+`
		 ORDER BY started_at DESC`,
		params...,
	)
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []SessionRecord
	for rows.Next() {
		var s SessionRecord
		if err := rows.Scan(&s.ID, &s.MediaServerID, &s.SessionKey, &s.StartedAt, &s.EndedAt); err != nil {
			return nil, fmt.Errorf("scanning session: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SessionSummary counts sessions in a window and how many distinct
// media servers they came from — the "6 sessions across 2 servers"
// figure, without making the caller aggregate a full list to get it.
// downloaderID scopes it the same way ListSessions does.
func (d *DB) SessionSummary(downloaderID int64, since, until time.Time) (sessions int64, servers int64, err error) {
	clause, args := mappedServersClause(downloaderID)
	params := append([]any{FormatTime(until), FormatTime(since)}, args...)

	err = d.conn.QueryRow(
		`SELECT COUNT(*), COUNT(DISTINCT media_server_id)
		 FROM session_history
		 WHERE started_at <= ? AND (ended_at IS NULL OR ended_at >= ?)`+clause,
		params...,
	).Scan(&sessions, &servers)
	if err != nil {
		return 0, 0, fmt.Errorf("summarising sessions: %w", err)
	}
	return sessions, servers, nil
}

// PruneSessionHistory deletes sessions that ended before the cutoff.
// Open sessions are never pruned regardless of age: one that has been
// running for longer than the retention window is still playing, and
// deleting it would strand it as permanently open.
func (d *DB) PruneSessionHistory(before time.Time) (int64, error) {
	result, err := d.conn.Exec(
		`DELETE FROM session_history WHERE ended_at IS NOT NULL AND ended_at < ?`,
		FormatTime(before),
	)
	if err != nil {
		return 0, fmt.Errorf("pruning session history: %w", err)
	}
	return result.RowsAffected()
}
