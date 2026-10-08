package db

import (
	"path/filepath"
	"testing"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func addTestDownloader(t *testing.T, d *DB, name string) int64 {
	t.Helper()
	id, err := d.CreateDownloader(Downloader{Name: name, Type: "qbittorrent", URL: "http://qb.invalid:8080", Enabled: true})
	if err != nil {
		t.Fatalf("creating downloader: %v", err)
	}
	return id
}
