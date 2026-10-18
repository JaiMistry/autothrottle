package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// A database from before Deluge: its downloaders table only allows
// qbittorrent and transmission.
func oldDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "old.db")
	conn, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	old := strings.Replace(schemaSQL, "'qbittorrent', 'transmission', 'deluge'", "'qbittorrent', 'transmission'", 1)
	if old == schemaSQL {
		t.Fatal("schema.sql's downloader types changed; update this test")
	}
	for _, stmt := range []string{
		old,
		`INSERT INTO media_servers (id, name, type, url, credential_encrypted) VALUES (1, 'Plex', 'plex', 'http://plex', 'x')`,
		`INSERT INTO downloaders (id, name, type, url, credential_encrypted, backup_download_limit) VALUES (1, 'qBittorrent', 'qbittorrent', 'http://qb', 'secret', 1500)`,
		`INSERT INTO downloaders (id, name, type, url) VALUES (2, 'Transmission', 'transmission', 'http://tr')`,
		// Deleted: AUTOINCREMENT must still never hand out 3 again.
		`INSERT INTO downloaders (id, name, type, url) VALUES (3, 'Gone', 'qbittorrent', 'http://gone')`,
		`DELETE FROM downloaders WHERE id = 3`,
		`INSERT INTO mappings (id, media_server_id, downloader_id) VALUES (1, 1, 1)`,
		`INSERT INTO events (level, message, downloader_id) VALUES ('info', 'throttled', 2)`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return path
}

func TestOpenRebuildsTheDownloadersTableForDeluge(t *testing.T) {
	path := oldDatabase(t)
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	dls, err := d.ListDownloaders()
	if err != nil {
		t.Fatal(err)
	}
	if len(dls) != 2 || dls[0].Name != "qBittorrent" || dls[0].CredentialEncrypted.String != "secret" || dls[0].BackupDownloadLimit != 1500 {
		t.Fatalf("downloaders after the rebuild = %+v", dls)
	}
	// Dropping the old table must not have cascaded to what refers to it.
	maps, err := d.ListMappings()
	if err != nil || len(maps) != 1 || maps[0].DownloaderID != 1 {
		t.Fatalf("mappings = %+v, %v", maps, err)
	}
	var eventDownloader sql.NullInt64
	if err := d.conn.QueryRow(`SELECT downloader_id FROM events`).Scan(&eventDownloader); err != nil || eventDownloader.Int64 != 2 {
		t.Fatalf("the event lost its downloader: %v, %v", eventDownloader, err)
	}

	id, err := d.CreateDownloader(Downloader{Name: "Deluge", Type: "deluge", URL: "http://deluge:8112", Enabled: true})
	if err != nil {
		t.Fatalf("a Deluge downloader is refused: %v", err)
	}
	if id != 4 {
		t.Errorf("new id = %d, want 4: a deleted downloader's id came back", id)
	}

	// The references still hold: deleting a downloader cascades as before.
	if err := d.DeleteDownloader(1); err != nil {
		t.Fatal(err)
	}
	if maps, _ := d.ListMappings(); len(maps) != 0 {
		t.Errorf("deleting the downloader left its mapping: %+v", maps)
	}
	var fk int
	if err := d.conn.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign keys are off after the rebuild: %d, %v", fk, err)
	}
	var indexes int
	if err := d.conn.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_mappings_downloader'`).Scan(&indexes); err != nil || indexes != 1 {
		t.Errorf("the mappings index is gone: %d, %v", indexes, err)
	}
}

func TestOpenLeavesACurrentDownloadersTableAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateDownloader(Downloader{Name: "Deluge", Type: "deluge", URL: "http://deluge:8112", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	d, err = Open(path)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer func() { _ = d.Close() }()
	if dls, _ := d.ListDownloaders(); len(dls) != 1 {
		t.Errorf("downloaders after reopening = %+v", dls)
	}
	if _, err := d.CreateDownloader(Downloader{Name: "Other", Type: "rtorrent", URL: "http://x", Enabled: true}); err == nil {
		t.Error("the type constraint is gone")
	}
}
