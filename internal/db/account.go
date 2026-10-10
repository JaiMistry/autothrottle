package db

import (
	"database/sql"
	"errors"
	"strings"
)

// ErrAccountExists is returned when an account is created while one
// already exists. Setup only ever runs once.
var ErrAccountExists = errors.New("an account already exists")

// GetAccount returns the stored login account, if one has been created.
func (d *DB) GetAccount() (username, passwordHash string, ok bool, err error) {
	err = d.conn.QueryRow(`SELECT username, password_hash FROM account WHERE id = 1`).Scan(&username, &passwordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return username, passwordHash, true, nil
}

// CreateAccount stores the one login account, refusing a second, so two
// racing setup submissions cannot both succeed.
func (d *DB) CreateAccount(username, passwordHash string) error {
	_, err := d.conn.Exec(`INSERT INTO account (id, username, password_hash) VALUES (1, ?, ?)`, username, passwordHash)
	if err != nil && (strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "PRIMARY KEY")) {
		return ErrAccountExists
	}
	return err
}
