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
	// Title and Device are best-effort display detail — what was
	// playing and where. A server may report neither.
	Title  string
	Device string
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
func (d *DB) OpenSession(mediaServerID int64, sessionKey, title, device string) error {
	var existing int64
	err := d.conn.QueryRow(
		`SELECT id FROM session_history
		 WHERE media_server_id = ? AND session_key = ? AND ended_at IS NULL`,
		mediaServerID, sessionKey,
	).Scan(&existing)
	if err == nil {
		// Already open. The live event stream carries no title or
		// device, so the reconciliation snapshot is the first thing
		// that knows them — fill them in rather than leaving the row
		// permanently blank.
		if title != "" || device != "" {
			_, err = d.conn.Exec(
				`UPDATE session_history SET title = COALESCE(NULLIF(?, ''), title),
				 device = COALESCE(NULLIF(?, ''), device) WHERE id = ?`,
				title, device, existing)
			if err != nil {
				return fmt.Errorf("updating session detail: %w", err)
			}
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("checking for an open session: %w", err)
	}

	_, err = d.conn.Exec(
		`INSERT INTO session_history (media_server_id, session_key, title, device) VALUES (?, ?, ?, ?)`,
		mediaServerID, sessionKey, title, device,
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
// sessions are no longer observable, so leaving them open would have
// them read as playing forever.
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

// OpenSessions returns a media server's sessions still in progress,
// newest first.
func (d *DB) OpenSessions(mediaServerID int64) ([]SessionRecord, error) {
	rows, err := d.conn.Query(
		`SELECT id, media_server_id, session_key, started_at, ended_at, title, device
		 FROM session_history
		 WHERE media_server_id = ? AND ended_at IS NULL
		 ORDER BY started_at DESC`,
		mediaServerID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []SessionRecord
	for rows.Next() {
		var s SessionRecord
		if err := rows.Scan(&s.ID, &s.MediaServerID, &s.SessionKey, &s.StartedAt, &s.EndedAt, &s.Title, &s.Device); err != nil {
			return nil, fmt.Errorf("scanning session: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
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
