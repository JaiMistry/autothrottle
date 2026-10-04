package db

import (
	"time"
)

// PausedSession is how long one session has been sitting paused, and
// where it was paused. See the paused_sessions table for why this is
// not simply held in memory.
type PausedSession struct {
	Since    time.Time
	Position time.Duration
}

// LoadPausedSessions reads every recorded pause for one media server,
// keyed by session key.
func (d *DB) LoadPausedSessions(mediaServerID int64) (map[string]PausedSession, error) {
	rows, err := d.conn.Query(
		`SELECT session_key, paused_since, position_ms FROM paused_sessions WHERE media_server_id = ?`,
		mediaServerID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[string]PausedSession{}
	for rows.Next() {
		var key, since string
		var positionMS int64
		if err := rows.Scan(&key, &since, &positionMS); err != nil {
			return nil, err
		}
		at, err := time.Parse(TimeFormat, since)
		if err != nil {
			// A row we cannot read is worse than no row: it would age a
			// session from the zero time and retire it instantly.
			continue
		}
		out[key] = PausedSession{Since: at, Position: time.Duration(positionMS) * time.Millisecond}
	}
	return out, rows.Err()
}

// SavePausedSession records when a session started being paused.
func (d *DB) SavePausedSession(mediaServerID int64, sessionKey string, state PausedSession) error {
	_, err := d.conn.Exec(
		`INSERT INTO paused_sessions (media_server_id, session_key, paused_since, position_ms)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(media_server_id, session_key)
		 DO UPDATE SET paused_since = excluded.paused_since, position_ms = excluded.position_ms`,
		mediaServerID, sessionKey, FormatTime(state.Since), state.Position.Milliseconds())
	return err
}

// DeletePausedSession forgets one session's pause, because it resumed
// or ended.
func (d *DB) DeletePausedSession(mediaServerID int64, sessionKey string) error {
	_, err := d.conn.Exec(
		`DELETE FROM paused_sessions WHERE media_server_id = ? AND session_key = ?`,
		mediaServerID, sessionKey)
	return err
}

// RetainPausedSessions drops every recorded pause for a media server
// whose session the server no longer lists.
func (d *DB) RetainPausedSessions(mediaServerID int64, present map[string]bool) error {
	rows, err := d.conn.Query(`SELECT session_key FROM paused_sessions WHERE media_server_id = ?`, mediaServerID)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			_ = rows.Close()
			return err
		}
		if !present[key] {
			stale = append(stale, key)
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, key := range stale {
		if err := d.DeletePausedSession(mediaServerID, key); err != nil {
			return err
		}
	}
	return nil
}
