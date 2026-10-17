package db

import (
	"fmt"
	"strings"
)

// SQLite cannot alter a CHECK constraint. Rebuild the parent table with
// foreign keys disabled outside the transaction, preserving dependent rows
// and their references (renaming the old table would rewrite those refs).
func (d *DB) migrateMediaServerTypes() (err error) {
	var definition string
	if err = d.conn.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'media_servers'`).Scan(&definition); err != nil {
		return fmt.Errorf("reading media server schema: %w", err)
	}
	if strings.Contains(definition, "'dispatcharr'") {
		return nil
	}
	if _, err = d.conn.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("preparing media server migration: %w", err)
	}
	defer func() {
		if _, enableErr := d.conn.Exec(`PRAGMA foreign_keys = ON`); enableErr != nil && err == nil {
			err = fmt.Errorf("restoring foreign keys: %w", enableErr)
		}
	}()
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Preserve the high water mark even if the last server was deleted.
	var sequence int64
	if err = tx.QueryRow(`SELECT coalesce((SELECT seq FROM sqlite_sequence WHERE name = 'media_servers'), 0)`).Scan(&sequence); err != nil {
		return err
	}
	create := strings.SplitN(schemaSQL, ";", 2)[0]
	create = strings.Replace(create, "media_servers", "media_servers_new", 1)
	for _, stmt := range []string{
		create,
		`INSERT INTO media_servers_new (id, name, type, url, credential_encrypted, skip_local_playback, enabled, created_at, updated_at)
		 SELECT id, name, type, url, credential_encrypted, skip_local_playback, enabled, created_at, updated_at FROM media_servers`,
		`DROP TABLE media_servers`,
		`ALTER TABLE media_servers_new RENAME TO media_servers`,
	} {
		if _, err = tx.Exec(stmt); err != nil {
			return fmt.Errorf("migrating media server types: %w", err)
		}
	}
	if _, err = tx.Exec(`UPDATE sqlite_sequence SET seq = max(seq, ?) WHERE name = 'media_servers'`, sequence); err != nil {
		return err
	}
	rows, err := tx.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	invalid := rows.Next()
	readErr := rows.Err()
	_ = rows.Close()
	if readErr != nil {
		return readErr
	}
	if invalid {
		return fmt.Errorf("media server migration found invalid foreign keys")
	}
	return tx.Commit()
}
