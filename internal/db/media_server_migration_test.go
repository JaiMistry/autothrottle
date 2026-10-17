package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestMediaServerTypeMigrationPreservesReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Replace(schemaSQL, "'jellyfin', 'dispatcharr'", "'jellyfin'", 1)
	if _, err := conn.Exec(legacy); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO media_servers(id,name,type,url,credential_encrypted,skip_local_playback,enabled) VALUES(1,'TV','jellyfin','http://tv','encrypted',1,0)`,
		`INSERT INTO media_servers(id,name,type,url,credential_encrypted) VALUES(77,'Deleted','plex','http://old','old')`,
		`DELETE FROM media_servers WHERE id=77`,
		`INSERT INTO downloaders(id,name,type,url) VALUES(1,'qbit','qbittorrent','http://qbit')`,
		`INSERT INTO mappings(id,media_server_id,downloader_id) VALUES(1,1,1)`,
		`INSERT INTO events(level,message,mapping_id,media_server_id,downloader_id) VALUES('info','kept',1,1,1)`,
		`INSERT INTO session_history(media_server_id,session_key,title) VALUES(1,'s','kept')`,
		`INSERT INTO paused_sessions(media_server_id,session_key,paused_since,position_ms) VALUES(1,'s','2026-01-01',123)`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	_ = conn.Close()
	for i := 0; i < 2; i++ { // Reopening is idempotent.
		d, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		server, err := d.GetMediaServer(1)
		if err != nil || server.Name != "TV" || server.CredentialEncrypted != "encrypted" || !server.SkipLocalPlayback || server.Enabled {
			t.Fatalf("migrated server = %+v, %v", server, err)
		}
		for _, table := range []string{"mappings", "events", "session_history", "paused_sessions"} {
			var count int
			if err := d.conn.QueryRow(`SELECT count(*) FROM ` + table + ` WHERE media_server_id=1`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("%s rows = %d, %v", table, count, err)
			}
		}
		var enabled int
		if err := d.conn.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
			t.Fatalf("foreign keys = %d, %v", enabled, err)
		}
		if i == 1 {
			id, err := d.CreateMediaServer(MediaServer{Name: "Live TV", Type: "dispatcharr", URL: "http://live", CredentialEncrypted: "key"})
			if err != nil || id <= 77 {
				t.Fatalf("new Dispatcharr id = %d, %v", id, err)
			}
			if err := d.DeleteMediaServer(1); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := d.conn.QueryRow(`SELECT count(*) FROM mappings`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("cascade = %d, %v", count, err)
			}
		}
		_ = d.Close()
	}
}
