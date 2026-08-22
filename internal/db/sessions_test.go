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

func openSessions(t *testing.T, d *DB, since, until time.Time) []SessionRecord {
	t.Helper()
	sessions, err := d.ListSessions(0, since, until)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	return sessions
}

func TestOpenAndCloseSession(t *testing.T) {
	d := newTestDB(t)
	server := addTestMediaServer(t, d, "Plex")
	window := time.Now().Add(-time.Hour)

	if err := d.OpenSession(server, "42"); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	sessions := openSessions(t, d, window, time.Now().Add(time.Hour))
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	if sessions[0].Ended() {
		t.Error("a freshly opened session must read as still playing")
	}

	if err := d.CloseSession(server, "42"); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	sessions = openSessions(t, d, window, time.Now().Add(time.Hour))
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
		if err := d.OpenSession(server, "42"); err != nil {
			t.Fatalf("OpenSession: %v", err)
		}
	}
	if got := len(openSessions(t, d, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))); got != 1 {
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
	if err := d.OpenSession(server, "42"); err != nil {
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

	if err := d.OpenSession(server, "1"); err != nil {
		t.Fatal(err)
	}
	if err := d.CloseSession(server, "1"); err != nil {
		t.Fatal(err)
	}
	if err := d.OpenSession(server, "1"); err != nil {
		t.Fatal(err)
	}

	sessions := openSessions(t, d, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
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

	if err := d.OpenSession(plex, "1"); err != nil {
		t.Fatal(err)
	}
	if err := d.OpenSession(jellyfin, "1"); err != nil {
		t.Fatal(err)
	}
	if err := d.CloseSessionsForServer(plex); err != nil {
		t.Fatalf("CloseSessionsForServer: %v", err)
	}

	for _, s := range openSessions(t, d, time.Now().Add(-time.Hour), time.Now().Add(time.Hour)) {
		if s.MediaServerID == plex && !s.Ended() {
			t.Error("the named server's sessions should be closed")
		}
		if s.MediaServerID == jellyfin && s.Ended() {
			t.Error("another server's sessions must be left alone")
		}
	}
}

// A crash leaves sessions open. They must not be credited with the
// whole outage — a server that sat powered off for a week would
// otherwise show a week-long session.
func TestCloseDanglingSessionsUsesLastKnownLiveness(t *testing.T) {
	d := newTestDB(t)
	server := addTestMediaServer(t, d, "Plex")
	downloader := addTestDownloader(t, d, "qb")

	start := time.Now().Add(-7 * 24 * time.Hour)
	lastAlive := start.Add(30 * time.Minute)

	if _, err := d.conn.Exec(
		`INSERT INTO session_history (media_server_id, session_key, started_at) VALUES (?, ?, ?)`,
		server, "42", FormatTime(start),
	); err != nil {
		t.Fatal(err)
	}
	addSample(t, d, downloader, lastAlive, 100, 10, false, 0)

	closed, err := d.CloseDanglingSessions()
	if err != nil {
		t.Fatalf("CloseDanglingSessions: %v", err)
	}
	if closed != 1 {
		t.Fatalf("closed %d sessions, want 1", closed)
	}

	sessions := openSessions(t, d, start.Add(-time.Hour), time.Now())
	if len(sessions) != 1 || !sessions[0].Ended() {
		t.Fatalf("session should be closed: %+v", sessions)
	}
	ended, err := time.Parse(TimeFormat, sessions[0].EndedAt.String)
	if err != nil {
		t.Fatal(err)
	}
	if !ended.Equal(lastAlive.UTC().Truncate(time.Millisecond)) {
		t.Errorf("ended_at = %s, want the last sample time %s", ended, lastAlive.UTC())
	}
}

// With no samples at all there is no liveness evidence, so the session
// gets zero duration rather than an invented one.
func TestCloseDanglingSessionsWithoutSamples(t *testing.T) {
	d := newTestDB(t)
	server := addTestMediaServer(t, d, "Plex")
	start := time.Now().Add(-48 * time.Hour)

	if _, err := d.conn.Exec(
		`INSERT INTO session_history (media_server_id, session_key, started_at) VALUES (?, ?, ?)`,
		server, "42", FormatTime(start),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CloseDanglingSessions(); err != nil {
		t.Fatalf("CloseDanglingSessions: %v", err)
	}

	sessions := openSessions(t, d, start.Add(-time.Hour), time.Now())
	if sessions[0].EndedAt.String != sessions[0].StartedAt {
		t.Errorf("ended_at = %q, want it to equal started_at %q",
			sessions[0].EndedAt.String, sessions[0].StartedAt)
	}
}

func TestListSessionsWindowIncludesOverlapsAndOpenSessions(t *testing.T) {
	d := newTestDB(t)
	server := addTestMediaServer(t, d, "Plex")
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	insert := func(key string, start time.Time, end *time.Time) {
		t.Helper()
		if end == nil {
			if _, err := d.conn.Exec(
				`INSERT INTO session_history (media_server_id, session_key, started_at) VALUES (?,?,?)`,
				server, key, FormatTime(start)); err != nil {
				t.Fatal(err)
			}
			return
		}
		if _, err := d.conn.Exec(
			`INSERT INTO session_history (media_server_id, session_key, started_at, ended_at) VALUES (?,?,?,?)`,
			server, key, FormatTime(start), FormatTime(*end)); err != nil {
			t.Fatal(err)
		}
	}

	before := base.Add(-5 * time.Hour)
	beforeEnd := base.Add(-4 * time.Hour)
	insert("old", before, &beforeEnd) // entirely before the window

	spanEnd := base.Add(30 * time.Minute)
	insert("spanning", base.Add(-30*time.Minute), &spanEnd) // straddles the window start

	insert("open", base.Add(10*time.Minute), nil) // still playing

	sessions := openSessions(t, d, base, base.Add(time.Hour))
	keys := map[string]bool{}
	for _, s := range sessions {
		keys[s.SessionKey] = true
	}
	if keys["old"] {
		t.Error("a session entirely outside the window must not be returned")
	}
	if !keys["spanning"] {
		t.Error("a session overlapping the window start must be returned")
	}
	if !keys["open"] {
		t.Error("an open session must be returned")
	}
}

func TestSessionSummaryCountsSessionsAndServers(t *testing.T) {
	d := newTestDB(t)
	plex := addTestMediaServer(t, d, "Plex")
	jellyfin := addTestMediaServer(t, d, "Jellyfin")

	for _, key := range []string{"1", "2", "3"} {
		if err := d.OpenSession(plex, key); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.OpenSession(jellyfin, "1"); err != nil {
		t.Fatal(err)
	}

	sessions, servers, err := d.SessionSummary(0, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("SessionSummary: %v", err)
	}
	if sessions != 4 {
		t.Errorf("sessions = %d, want 4", sessions)
	}
	if servers != 2 {
		t.Errorf("servers = %d, want 2", servers)
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

	sessions := openSessions(t, d, old.Add(-time.Hour), time.Now().Add(time.Hour))
	if len(sessions) != 1 || sessions[0].SessionKey != "still-open" {
		t.Errorf("the open session must survive pruning: %+v", sessions)
	}
}

// Analytics is presented per downloader, so a server that does not
// throttle the selected downloader must not appear beside its chart.
func TestSessionQueriesScopeToMappedServers(t *testing.T) {
	d := newTestDB(t)
	mapped := addTestMediaServer(t, d, "Living Room Plex")
	unmapped := addTestMediaServer(t, d, "Attic Jellyfin")
	qb := addTestDownloader(t, d, "qb")
	other := addTestDownloader(t, d, "transmission")

	if _, err := d.CreateMapping(mapped, qb); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateMapping(unmapped, other); err != nil {
		t.Fatal(err)
	}
	if err := d.OpenSession(mapped, "1"); err != nil {
		t.Fatal(err)
	}
	if err := d.OpenSession(unmapped, "1"); err != nil {
		t.Fatal(err)
	}

	since, until := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)

	scoped, err := d.ListSessions(qb, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 1 || scoped[0].MediaServerID != mapped {
		t.Fatalf("scoped listing = %+v, want only the mapped server's session", scoped)
	}

	sessions, servers, err := d.SessionSummary(qb, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || servers != 1 {
		t.Errorf("scoped summary = %d sessions across %d servers, want 1 and 1", sessions, servers)
	}

	// Zero still means "everything", for a view not scoped to one
	// downloader.
	all, err := d.ListSessions(0, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("unscoped listing returned %d sessions, want 2", len(all))
	}
}

// A disabled mapping means that server's playback is not driving this
// downloader, so its sessions should not be attributed to it.
func TestSessionScopeIgnoresDisabledMappings(t *testing.T) {
	d := newTestDB(t)
	server := addTestMediaServer(t, d, "Plex")
	qb := addTestDownloader(t, d, "qb")

	mappingID, err := d.CreateMapping(server, qb)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetMappingEnabled(mappingID, false); err != nil {
		t.Fatal(err)
	}
	if err := d.OpenSession(server, "1"); err != nil {
		t.Fatal(err)
	}

	scoped, err := d.ListSessions(qb, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 0 {
		t.Errorf("got %+v, want nothing — the mapping is disabled", scoped)
	}
}
