package db

import (
	"database/sql"
	"errors"
)

// GetSetting returns the raw stored value for key. ok is false when the
// key has never been written — callers apply their own default rather
// than the zero value being mistaken for a real setting.
func (d *DB) GetSetting(key string) (value string, ok bool, err error) {
	err = d.conn.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// SetSetting writes key, overwriting any existing value.
func (d *DB) SetSetting(key, value string) error {
	_, err := d.conn.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value,
		   updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`,
		key, value,
	)
	return err
}
