package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/db"
)

func newDataDir(t *testing.T) (string, *db.DB) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "autothrottle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return dir, database
}

func TestAFolderNobodySetUpHoldsNoSettings(t *testing.T) {
	if holdsSettings(t.TempDir()) {
		t.Error("an empty folder")
	}
	dir, database := newDataDir(t)
	// What a first start leaves: the run, its events, the pause switch.
	if _, err := database.StartRun(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := database.InsertEvent(db.Event{Level: "info", Message: "started"}); err != nil {
		t.Fatal(err)
	}
	if holdsSettings(dir) {
		t.Error("a first start's bookkeeping")
	}
}

func TestAnAccountOrConnectionIsWorthKeeping(t *testing.T) {
	dir, database := newDataDir(t)
	if err := database.CreateAccount("admin", "hash"); err != nil {
		t.Fatal(err)
	}
	if !holdsSettings(dir) {
		t.Error("an account")
	}

	// Signed in from the environment, so no account: a connection alone.
	dir, database = newDataDir(t)
	if _, err := database.CreateMediaServer(db.MediaServer{Name: "Plex", Type: "plex", URL: "http://plex:32400", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if !holdsSettings(dir) {
		t.Error("a media server")
	}

	dir, database = newDataDir(t)
	if _, err := database.CreateDownloader(db.Downloader{Name: "qBittorrent", Type: "qbittorrent", URL: "http://qb:8080", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if !holdsSettings(dir) {
		t.Error("a download client")
	}
}

// Unreadable is treated as worth asking about.
func TestAnUnreadableDatabaseIsKept(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "autothrottle.db"), []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !holdsSettings(dir) {
		t.Error("an unreadable database was taken for an empty one")
	}
}
