// Package db is the SQLite data layer.
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
	// onEvents runs after the event log changes, so an open Logs page
	// can show it. Set once, before anything writes.
	onEvents func()
}

// OnEventsChanged registers fn to run whenever an event is written or
// the log is cleared.
func (d *DB) OnEventsChanged(fn func()) { d.onEvents = fn }

func (d *DB) eventsChanged() {
	if d.onEvents != nil {
		d.onEvents()
	}
}

// Open opens (creating if needed) the SQLite database at path and
// applies the schema.
func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	// One connection: SQLite has one writer, and this avoids SQLITE_BUSY.
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

// migrate adds columns CREATE TABLE IF NOT EXISTS cannot. Each is
// idempotent: a duplicate-column error means already applied.
func (d *DB) migrate() error {
	for _, stmt := range []string{
		`ALTER TABLE session_history ADD COLUMN title TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE session_history ADD COLUMN device TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := d.conn.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("migrating: %w", err)
		}
	}
	// Drop sessions stored as the raw token, from before hashing.
	if _, err := d.conn.Exec(`DELETE FROM sessions WHERE length(token) <> 64`); err != nil {
		return fmt.Errorf("migrating sessions: %w", err)
	}
	return nil
}
