package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// sessionKey is what is stored for a login token: its SHA-256, so a copy
// of the database cannot sign anyone in. The token is 32 random bytes,
// so a plain hash suffices.
func sessionKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateAuthSession records a login token and when it lapses.
func (d *DB) CreateAuthSession(token string, expiresAt time.Time) error {
	_, err := d.conn.Exec(
		`INSERT INTO sessions (token, expires_at) VALUES (?, ?)`,
		sessionKey(token), FormatTime(expiresAt),
	)
	if err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	return nil
}

// AuthSessionValid reports whether a token is known and unexpired,
// deleting an expired row as it finds it.
func (d *DB) AuthSessionValid(token string, now time.Time) (bool, error) {
	var raw string
	key := sessionKey(token)
	err := d.conn.QueryRow(`SELECT expires_at FROM sessions WHERE token = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading session: %w", err)
	}

	expires, err := time.Parse(TimeFormat, raw)
	if err != nil || !now.Before(expires) {
		_, _ = d.conn.Exec(`DELETE FROM sessions WHERE token = ?`, key)
		return false, nil
	}
	return true, nil
}

// DeleteAuthSession revokes one token.
func (d *DB) DeleteAuthSession(token string) error {
	_, err := d.conn.Exec(`DELETE FROM sessions WHERE token = ?`, sessionKey(token))
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
