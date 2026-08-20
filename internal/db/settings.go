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

// ListSettings returns every stored setting, for a configuration page
// that wants to show what's been explicitly overridden.
func (d *DB) ListSettings() (map[string]string, error) {
	rows, err := d.conn.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}
