package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A pause's clock survives a restart, per media server, and goes with
// its server.
func TestPausedSessionsRoundTrip(t *testing.T) {
	d := newTestDB(t)
	ms := addTestMediaServer(t, d, "Plex")
	since := time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC)

	for key, pos := range map[string]time.Duration{"a": time.Minute, "b": 2 * time.Minute} {
		if err := d.SavePausedSession(ms, key, PausedSession{Since: since, Position: pos}); err != nil {
			t.Fatal(err)
		}
	}
	// Saving again replaces, rather than duplicating.
	if err := d.SavePausedSession(ms, "a", PausedSession{Since: since, Position: 3 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	got, err := d.LoadPausedSessions(ms)
	if err != nil || len(got) != 2 || !got["a"].Since.Equal(since) || got["a"].Position != 3*time.Minute || got["b"].Position != 2*time.Minute {
		t.Fatalf("loaded %+v, %v", got, err)
	}
	if other, _ := d.LoadPausedSessions(ms + 1); len(other) != 0 {
		t.Errorf("another server sees %+v", other)
	}

	if err := d.RetainPausedSessions(ms, map[string]bool{"a": true}); err != nil {
		t.Fatal(err)
	}
	if err := d.DeletePausedSession(ms, "a"); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.LoadPausedSessions(ms); len(got) != 0 {
		t.Errorf("after retain and delete: %+v", got)
	}

	_ = d.SavePausedSession(ms, "c", PausedSession{Since: since})
	if err := d.DeleteMediaServer(ms); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.LoadPausedSessions(ms); len(got) != 0 {
		t.Errorf("a deleted server's pauses outlived it: %+v", got)
	}
}

// Every call reports a failed database rather than panicking or
// pretending: callers turn these into error pages and log lines.
func TestEveryCallFailsOnAClosedDatabase(t *testing.T) {
	d := newTestDB(t)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	calls := map[string]func() error{
		"GetAccount":             func() error { _, _, _, err := d.GetAccount(); return err },
		"CreateAccount":          func() error { return d.CreateAccount("u", "h") },
		"CreateMediaServer":      func() error { _, err := d.CreateMediaServer(MediaServer{}); return err },
		"ListMediaServers":       func() error { _, err := d.ListMediaServers(); return err },
		"GetMediaServer":         func() error { _, err := d.GetMediaServer(1); return err },
		"UpdateMediaServer":      func() error { return d.UpdateMediaServer(MediaServer{}) },
		"DeleteMediaServer":      func() error { return d.DeleteMediaServer(1) },
		"CreateDownloader":       func() error { _, err := d.CreateDownloader(Downloader{}); return err },
		"ListDownloaders":        func() error { _, err := d.ListDownloaders(); return err },
		"GetDownloader":          func() error { _, err := d.GetDownloader(1); return err },
		"UpdateDownloader":       func() error { return d.UpdateDownloader(Downloader{}) },
		"DeleteDownloader":       func() error { return d.DeleteDownloader(1) },
		"CreateMapping":          func() error { _, err := d.CreateMapping(1, 1); return err },
		"ListMappings":           func() error { _, err := d.ListMappings(); return err },
		"EnableMappingsForMS":    func() error { return d.EnableMappingsForMediaServer(1) },
		"EnableMappingsForDL":    func() error { return d.EnableMappingsForDownloader(1) },
		"SetMappingForPair":      func() error { return d.SetMappingForPair(1, 1, true) },
		"InsertEvent":            func() error { return d.InsertEvent(Event{Level: "info"}) },
		"ListEvents":             func() error { _, err := d.ListEvents(10); return err },
		"CountEvents":            func() error { _, err := d.CountEvents(EventFilter{}); return err },
		"CountEventsByLevel":     func() error { _, err := d.CountEventsByLevel(EventFilter{}); return err },
		"PruneEvents":            func() error { _, err := d.PruneEvents(now); return err },
		"DeleteAllEvents":        func() error { _, err := d.DeleteAllEvents(); return err },
		"LoadPausedSessions":     func() error { _, err := d.LoadPausedSessions(1); return err },
		"SavePausedSession":      func() error { return d.SavePausedSession(1, "k", PausedSession{}) },
		"DeletePausedSession":    func() error { return d.DeletePausedSession(1, "k") },
		"RetainPausedSessions":   func() error { return d.RetainPausedSessions(1, nil) },
		"CreateAuthSession":      func() error { return d.CreateAuthSession("t", now) },
		"AuthSessionValid":       func() error { _, err := d.AuthSessionValid("t", now); return err },
		"DeleteAuthSession":      func() error { return d.DeleteAuthSession("t") },
		"PruneAuthSessions":      func() error { _, err := d.PruneAuthSessions(now); return err },
		"StartRun":               func() error { _, err := d.StartRun(now); return err },
		"Heartbeat":              func() error { return d.Heartbeat(1, now) },
		"OpenThrottlePeriod":     func() error { return d.OpenThrottlePeriod(now) },
		"CloseThrottlePeriod":    func() error { return d.CloseThrottlePeriod(now) },
		"ThrottlePeriods":        func() error { _, err := d.ThrottlePeriods(now, now); return err },
		"Runs":                   func() error { _, err := d.Runs(now, now); return err },
		"PruneHistory":           func() error { _, err := d.PruneHistory(now); return err },
		"OpenSession":            func() error { return d.OpenSession(1, "k", "", "") },
		"CloseSession":           func() error { return d.CloseSession(1, "k") },
		"CloseSessionsForServer": func() error { return d.CloseSessionsForServer(1) },
		"OpenSessions":           func() error { _, err := d.OpenSessions(1); return err },
		"PruneSessionHistory":    func() error { _, err := d.PruneSessionHistory(now); return err },
		"GetSetting":             func() error { _, _, err := d.GetSetting("k"); return err },
		"SetSetting":             func() error { return d.SetSetting("k", "v") },
		"migrate":                d.migrate,
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s succeeded on a closed database", name)
		}
	}
}

// A row the code cannot read — a corrupted value, or a database edited
// by hand — is an error, never a silently skipped or zeroed entry.
func TestUnreadableRowsAreErrors(t *testing.T) {
	d := newTestDB(t)
	ms := addTestMediaServer(t, d, "Plex")
	dl := addTestDownloader(t, d, "qb")
	if _, err := d.CreateMapping(ms, dl); err != nil {
		t.Fatal(err)
	}
	corrupt := func(stmt string, args ...any) {
		t.Helper()
		if _, err := d.conn.Exec(stmt, args...); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	corrupt(`UPDATE media_servers SET enabled = 'maybe'`)
	corrupt(`UPDATE downloaders SET enabled = 'maybe'`)
	corrupt(`UPDATE mappings SET enabled = 'maybe'`)
	// As a hand edit would: with the foreign-key check off.
	corrupt(`PRAGMA foreign_keys = OFF`)
	corrupt(`INSERT INTO events (level, message, mapping_id) VALUES ('info', 'm', 'not a number')`)
	corrupt(`PRAGMA foreign_keys = ON`)
	corrupt(`INSERT INTO runs (started_at, last_seen_at) VALUES ('2026-10-01T00:00:00.000Z', 'later')`)
	corrupt(`INSERT INTO throttle_periods (started_at, ended_at) VALUES ('2026-garbled', NULL)`)

	since, until := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	reads := map[string]func() error{
		"ListMediaServers": func() error { _, err := d.ListMediaServers(); return err },
		"GetMediaServer":   func() error { _, err := d.GetMediaServer(ms); return err },
		"ListDownloaders":  func() error { _, err := d.ListDownloaders(); return err },
		"GetDownloader":    func() error { _, err := d.GetDownloader(dl); return err },
		"ListMappings":     func() error { _, err := d.ListMappings(); return err },
		"ListEvents":       func() error { _, err := d.ListEvents(10); return err },
		"Runs":             func() error { _, err := d.Runs(since, until); return err },
		"ThrottlePeriods":  func() error { _, err := d.ThrottlePeriods(since, until); return err },
	}
	for name, read := range reads {
		if err := read(); err == nil {
			t.Errorf("%s read a corrupted row without error", name)
		}
	}

	// A pause with an unreadable time is skipped, not failed: read as
	// paused since the zero time, it would be retired at once.
	corrupt(`INSERT INTO paused_sessions (media_server_id, session_key, paused_since, position_ms) VALUES (?, 'bad', 'yesterday', 0), (?, 'good', '2026-10-01T00:00:00.000Z', 0)`, ms, ms)
	if got, err := d.LoadPausedSessions(ms); err != nil || len(got) != 1 || got["good"].Since.IsZero() {
		t.Errorf("loaded %+v, %v", got, err)
	}
	corrupt(`INSERT INTO paused_sessions (media_server_id, session_key, paused_since, position_ms) VALUES (?, 'far', '2026-10-01T00:00:00.000Z', 'far')`, ms)
	if _, err := d.LoadPausedSessions(ms); err == nil {
		t.Error("a corrupted pause position was read")
	}
}

func TestOpenFailures(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "missing", "dir", "x.db")); err == nil {
		t.Error("opened a database in a directory that does not exist")
	}
	notADB := filepath.Join(t.TempDir(), "x.db")
	if err := os.WriteFile(notADB, []byte(strings.Repeat("not sqlite ", 200)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(notADB); err == nil {
		t.Error("opened a file that is not a database")
	}

	// A schema change that fails is reported, not ignored.
	d := newTestDB(t)
	if _, err := d.conn.Exec(`DROP TABLE sessions`); err != nil {
		t.Fatal(err)
	}
	if err := d.migrate(); err == nil {
		t.Error("migrating without a sessions table succeeded")
	}
}

// Each write a multi-step call makes can fail on its own; the call
// reports which.
func TestPartialWriteFailures(t *testing.T) {
	refuse := func(d *DB, event, table string) {
		t.Helper()
		stmt := `CREATE TRIGGER refuse_` + strings.ToLower(event) + `_` + table + ` BEFORE ` + event + ` ON ` + table + ` BEGIN SELECT RAISE(ABORT, 'refused'); END`
		if _, err := d.conn.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	now := time.Now()

	for table, want := range map[string]string{"runs": "reading the last run", "throttle_periods": "closing dangling throttle periods", "session_history": "closing dangling sessions"} {
		d := newTestDB(t)
		if _, err := d.conn.Exec(`ALTER TABLE ` + table + ` RENAME TO gone`); err != nil {
			t.Fatal(err)
		}
		if _, err := d.StartRun(now); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("without %s: %v", table, err)
		}
	}
	d := newTestDB(t)
	refuse(d, "INSERT", "runs")
	if _, err := d.StartRun(now); err == nil || !strings.Contains(err.Error(), "recording the run") {
		t.Errorf("refused run: %v", err)
	}

	d = newTestDB(t)
	ms := addTestMediaServer(t, d, "Plex")
	if err := d.OpenSession(ms, "a", "", ""); err != nil {
		t.Fatal(err)
	}
	refuse(d, "UPDATE", "session_history")
	refuse(d, "INSERT", "session_history")
	if err := d.OpenSession(ms, "a", "Andor", "TV"); err == nil {
		t.Error("a refused detail update succeeded")
	}
	if err := d.OpenSession(ms, "b", "", ""); err == nil {
		t.Error("a refused session start succeeded")
	}

	d = newTestDB(t)
	ms = addTestMediaServer(t, d, "Plex")
	_ = d.SavePausedSession(ms, "gone", PausedSession{Since: now})
	refuse(d, "DELETE", "paused_sessions")
	if err := d.RetainPausedSessions(ms, nil); err == nil {
		t.Error("a refused delete succeeded")
	}
}
