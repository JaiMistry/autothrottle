package main

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// holdsSettings reports whether a data folder has anything someone put
// there: an account, a media server or a download client. The service
// makes the folder on its first start, so one nobody ever opened the page
// for holds only its own bookkeeping, and an uninstall needn't ask about
// keeping it. Anything it can't read counts as settings: asking is the
// safe side.
func holdsSettings(dir string) bool {
	path := filepath.Join(dir, "autothrottle.db")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false
	}
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return true
	}
	defer func() { _ = conn.Close() }()
	var n int
	err = conn.QueryRow(`SELECT (SELECT COUNT(*) FROM account) +
		(SELECT COUNT(*) FROM media_servers) + (SELECT COUNT(*) FROM downloaders)`).Scan(&n)
	return err != nil || n > 0
}
