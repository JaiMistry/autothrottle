package db

import (
	"testing"
	"time"
)

func addTestMediaServer(t *testing.T, d *DB, name string) int64 {
	t.Helper()
	id, err := d.CreateMediaServer(MediaServer{
		Name: name, Type: "plex", URL: "http://plex.invalid:32400",
		CredentialEncrypted: "x", Enabled: true,
	})
	if err != nil {
		t.Fatalf("creating media server: %v", err)
	}
	return id
}

// allSessions reads every row, open or closed, newest first.
func allSessions(t *testing.T, d *DB) []SessionRecord {
	t.Helper()
	rows, err := d.conn.Query(`SELECT id, media_server_id, session_key, started_at, ended_at, title, device
		FROM session_history ORDER BY started_at DESC, id DESC`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []SessionRecord
	for rows.Next() {
		var s SessionRecord
		if err := rows.Scan(&s.ID, &s.MediaServerID, &s.SessionKey, &s.StartedAt, &s.EndedAt, &s.Title, &s.Device); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestOpenAndCloseSession(t *testing.T) {
	d := newTestDB(t)
	server := addTestMediaServer(t, d, "Plex")

	if err := d.OpenSession(server, "42", "", ""); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	sessions := allSessions(t, d)
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	if sessions[0].Ended() {
		t.Error("a freshly opened session must read as still playing")
	}

	if err := d.CloseSession(server, "42"); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	sessions = allSessions(t, d)
	if len(sessions) != 1 || !sessions[0].Ended() {
		t.Fatalf("session should be closed: %+v", sessions)
	}
}

// The scheduler learns a session is playing from both the event stream
// and the reconciliation snapshot, and they overlap constantly. A
// second open must not create a second row.
func TestOpenSessionIsIdempotent(t *testing.T) {
	d := newTestDB(t)
	server := addTestMediaServer(t, d, "Plex")

	for i := 0; i < 3; i++ {
		if err := d.OpenSession(server, "42", "", ""); err != nil {
			t.Fatalf("OpenSession: %v", err)
		}
	}
	if got := len(allSessions(t, d)); got != 1 {
		t.Errorf("got %d rows, want 1", got)
	}
}

func TestCloseSessionIsIdempotent(t *testing.T) {
	d := newTestDB(t)
	server := addTestMediaServer(t, d, "Plex")

	// Closing something never opened is not an error.
	if err := d.CloseSession(server, "never"); err != nil {
		t.Fatalf("closing an unknown session: %v", err)
	}
	if err := d.OpenSession(server, "42", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := d.CloseSession(server, "42"); err != nil {
		t.Fatal(err)
	}
	if err := d.CloseSession(server, "42"); err != nil {
		t.Fatalf("closing twice: %v", err)
	}
}

// Media servers reuse session keys, so a new session with a key seen
// before must open a fresh row rather than reviving the old one.
func TestReusedSessionKeyOpensNewRow(t *testing.T) {
	d := newTestDB(t)
	server := addTestMediaServer(t, d, "Plex")

	if err := d.OpenSession(server, "1", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := d.CloseSession(server, "1"); err != nil {
		t.Fatal(err)
	}
	if err := d.OpenSession(server, "1", "", ""); err != nil {
		t.Fatal(err)
	}

	sessions := allSessions(t, d)
	if len(sessions) != 2 {
		t.Fatalf("got %d rows, want 2", len(sessions))
	}
	var open int
	for _, s := range sessions {
		if !s.Ended() {
			open++
		}
	}
	if open != 1 {
		t.Errorf("got %d open sessions, want exactly 1", open)
	}
}

func TestCloseSessionsForServerOnlyAffectsThatServer(t *testing.T) {
	d := newTestDB(t)
	plex := addTestMediaServer(t, d, "Plex")
	jellyfin := addTestMediaServer(t, d, "Jellyfin")

	if err := d.OpenSession(plex, "1", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := d.OpenSession(jellyfin, "1", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := d.CloseSessionsForServer(plex); err != nil {
		t.Fatalf("CloseSessionsForServer: %v", err)
	}

	for _, s := range allSessions(t, d) {
		if s.MediaServerID == plex && !s.Ended() {
			t.Error("the named server's sessions should be closed")
		}
		if s.MediaServerID == jellyfin && s.Ended() {
			t.Error("another server's sessions must be left alone")
		}
	}
}

// An open session older than the retention window is still playing;
// deleting it would strand it as permanently open.
func TestPruneSessionHistoryKeepsOpenSessions(t *testing.T) {
	d := newTestDB(t)
	server := addTestMediaServer(t, d, "Plex")
	old := time.Now().Add(-100 * 24 * time.Hour)
	oldEnd := old.Add(time.Hour)

	if _, err := d.conn.Exec(
		`INSERT INTO session_history (media_server_id, session_key, started_at, ended_at) VALUES (?,?,?,?)`,
		server, "closed", FormatTime(old), FormatTime(oldEnd)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.conn.Exec(
		`INSERT INTO session_history (media_server_id, session_key, started_at) VALUES (?,?,?)`,
		server, "still-open", FormatTime(old)); err != nil {
		t.Fatal(err)
	}

	deleted, err := d.PruneSessionHistory(time.Now().Add(-30 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("PruneSessionHistory: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted %d rows, want 1", deleted)
	}

	sessions := allSessions(t, d)
	if len(sessions) != 1 || sessions[0].SessionKey != "still-open" {
		t.Errorf("the open session must survive pruning: %+v", sessions)
	}
}

func TestOpenSessionsListsOnlyThatServersOpenSessions(t *testing.T) {
	d := newTestDB(t)
	plex := addTestMediaServer(t, d, "Plex")
	other := addTestMediaServer(t, d, "Other")
	for _, key := range []string{"a", "b"} {
		if err := d.OpenSession(plex, key, "Title "+key, "TV"); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.OpenSession(other, "c", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := d.CloseSession(plex, "a"); err != nil {
		t.Fatal(err)
	}

	open, err := d.OpenSessions(plex)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].SessionKey != "b" || open[0].Title != "Title b" {
		t.Fatalf("open sessions = %+v, want only b", open)
	}
}
