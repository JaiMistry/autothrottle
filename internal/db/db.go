// Package db wraps the SQLite-backed data layer: media servers,
// downloaders, the mappings between them, and the event history.
package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// DB wraps a SQLite connection with the app's data access methods.
type DB struct {
	conn *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and
// applies the schema.
func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	// SQLite handles one writer at a time; a single connection avoids
	// SQLITE_BUSY under concurrent goroutines rather than papering over
	// it with busy-timeout retries.
	conn.SetMaxOpenConns(1)

	if _, err := conn.Exec(schemaSQL); err != nil {
		_ = conn.Close() // already returning the more relevant schema error
		return nil, fmt.Errorf("applying schema: %w", err)
	}

	database := &DB{conn: conn}
	if err := database.migrate(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return database, nil
}

// Close closes the underlying connection.
func (d *DB) Close() error {
	return d.conn.Close()
}

// migrate applies the additive schema changes that CREATE TABLE IF NOT
// EXISTS cannot: a column added to a table that already exists on a
// running install. Each is idempotent — a duplicate-column error means
// it is already applied, which is success rather than failure.
func (d *DB) migrate() error {
	for _, stmt := range []string{
		`ALTER TABLE session_history ADD COLUMN title TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE session_history ADD COLUMN device TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := d.conn.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("migrating: %w", err)
		}
	}
	return nil
}
