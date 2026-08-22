package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// CreateAuthSession records a login token and when it lapses.
func (d *DB) CreateAuthSession(token string, expiresAt time.Time) error {
	_, err := d.conn.Exec(
		`INSERT INTO sessions (token, expires_at) VALUES (?, ?)`,
		token, FormatTime(expiresAt),
	)
	if err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	return nil
}

// AuthSessionValid reports whether a token is known and unexpired.
//
// Expired rows are deleted as they are found rather than on a timer:
// the table only grows on login, so the natural moment to clean one up
// is the request that discovers it.
func (d *DB) AuthSessionValid(token string, now time.Time) (bool, error) {
	var raw string
	err := d.conn.QueryRow(`SELECT expires_at FROM sessions WHERE token = ?`, token).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading session: %w", err)
	}

	expires, err := time.Parse(TimeFormat, raw)
	if err != nil || !now.Before(expires) {
		_, _ = d.conn.Exec(`DELETE FROM sessions WHERE token = ?`, token)
		return false, nil
	}
	return true, nil
}

// DeleteAuthSession revokes one token.
func (d *DB) DeleteAuthSession(token string) error {
	_, err := d.conn.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	if err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	return nil
}

// PruneAuthSessions removes everything already lapsed.
func (d *DB) PruneAuthSessions(now time.Time) (int64, error) {
	result, err := d.conn.Exec(`DELETE FROM sessions WHERE expires_at < ?`, FormatTime(now))
	if err != nil {
		return 0, fmt.Errorf("pruning sessions: %w", err)
	}
	return result.RowsAffected()
}
