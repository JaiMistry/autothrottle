// Package db is the SQLite data layer.
package db

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
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
		`ALTER TABLE downloaders ADD COLUMN alternative_speed INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := d.conn.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("migrating: %w", err)
		}
	}
	// Drop sessions stored as the raw token, from before hashing.
	if _, err := d.conn.Exec(`DELETE FROM sessions WHERE length(token) <> 64`); err != nil {
		return fmt.Errorf("migrating sessions: %w", err)
	}
	// Deluge joined the download client types.
	return d.rebuildTable("downloaders", "'deluge'")
}

// rebuildTable recreates table from schema.sql's definition, keeping its
// rows, when the stored definition lacks marker: SQLite can't change a
// CHECK constraint in place. SQLite's own recipe: a new table, the rows
// copied over, the old one dropped and the new one renamed, with foreign
// keys off throughout so the drop cascades nothing. References to the
// table from others are by name, so they follow the rename.
func (d *DB) rebuildTable(table, marker string) (err error) {
	ctx := context.Background()
	var stored string
	if err := d.conn.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&stored); err != nil {
		return fmt.Errorf("reading the %s table: %w", table, err)
	}
	if strings.Contains(stored, marker) {
		return nil
	}
	head := "CREATE TABLE IF NOT EXISTS " + table + " ("
	start := strings.Index(schemaSQL, head)
	if start < 0 {
		return fmt.Errorf("schema.sql has no %s table", table)
	}
	create := schemaSQL[start : start+strings.Index(schemaSQL[start:], ");")+2]
	create = "CREATE TABLE " + table + "_new (" + strings.TrimPrefix(create, head)

	// One connection throughout: foreign_keys is a per-connection setting,
	// and can't change inside a transaction.
	conn, err := d.conn.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("rebuilding %s: %w", table, err)
	}
	defer func() {
		if _, onErr := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); onErr != nil && err == nil {
			err = fmt.Errorf("rebuilding %s: %w", table, onErr)
		}
	}()

	var columns []string
	rows, err := conn.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return fmt.Errorf("rebuilding %s: %w", table, err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return fmt.Errorf("rebuilding %s: %w", table, err)
		}
		columns = append(columns, name)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("rebuilding %s: %w", table, err)
	}
	cols := strings.Join(columns, ", ")

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("rebuilding %s: %w", table, err)
	}
	defer func() { _ = tx.Rollback() }()
	// AUTOINCREMENT never reuses an id, deleted rows' included; the copy
	// alone would forget the ones above the highest left.
	var seq sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT seq FROM sqlite_sequence WHERE name = ?`, table).Scan(&seq); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("rebuilding %s: %w", table, err)
	}
	for _, stmt := range []string{
		create,
		"INSERT INTO " + table + "_new (" + cols + ") SELECT " + cols + " FROM " + table,
		"DROP TABLE " + table,
		"ALTER TABLE " + table + "_new RENAME TO " + table,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("rebuilding %s: %w", table, err)
		}
	}
	if seq.Valid {
		if _, err := tx.ExecContext(ctx, `UPDATE sqlite_sequence SET seq = max(seq, ?) WHERE name = ?`, seq.Int64, table); err != nil {
			return fmt.Errorf("rebuilding %s: %w", table, err)
		}
	}
	var broken int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&broken); err != nil {
		return fmt.Errorf("rebuilding %s: %w", table, err)
	}
	if broken > 0 {
		return fmt.Errorf("rebuilding %s: %d rows lost what they refer to", table, broken)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("rebuilding %s: %w", table, err)
	}
	// Indexes go with a dropped table; the schema brings them back.
	if _, err := conn.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("rebuilding %s: %w", table, err)
	}
	return nil
}
