package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JaiMistry/autothrottle/internal/schedule"
)

func TestWindows(t *testing.T) {
	d := newTestDB(t)
	qb := addTestDownloader(t, d, "qBittorrent")
	tr := addTestDownloader(t, d, "Transmission")

	night := schedule.Window{Name: "Night", Days: schedule.EveryDay, Start: 23 * 60, End: 7 * 60, Throttle: false}
	work := schedule.Window{Days: schedule.Weekdays, Start: 9 * 60, End: 17 * 60, DownloadKBps: 2000, UploadKBps: 500, Throttle: true}
	nightID, err := d.CreateWindow(qb, night)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateWindow(qb, work); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateWindow(tr, work); err != nil {
		t.Fatal(err)
	}

	got, err := d.DownloaderWindows(qb)
	if err != nil {
		t.Fatal(err)
	}
	night.ID = nightID
	// In the order they start: work at 09:00, then night at 23:00.
	if len(got) != 2 || got[0].Start != work.Start || got[1] != night {
		t.Fatalf("windows = %+v", got)
	}
	if got[0].DownloadKBps != 2000 || got[0].UploadKBps != 500 || !got[0].Throttle || got[0].Days != schedule.Weekdays {
		t.Fatalf("work window = %+v", got[0])
	}

	night.DownloadKBps = 100
	if err := d.UpdateWindow(qb, night); err != nil {
		t.Fatal(err)
	}
	// Another downloader's window can't be changed or removed through this one.
	if err := d.UpdateWindow(tr, night); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("updating through another downloader: %v", err)
	}
	if err := d.DeleteWindow(tr, nightID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleting through another downloader: %v", err)
	}

	all, err := d.ListWindows()
	if err != nil {
		t.Fatal(err)
	}
	if len(all[qb]) != 2 || len(all[tr]) != 1 || all[qb][1].DownloadKBps != 100 {
		t.Fatalf("all windows = %+v", all)
	}

	if err := d.DeleteWindow(qb, nightID); err != nil {
		t.Fatal(err)
	}
	// Deleting a downloader takes its windows with it.
	if err := d.DeleteDownloader(tr); err != nil {
		t.Fatal(err)
	}
	all, err = d.ListWindows()
	if err != nil {
		t.Fatal(err)
	}
	if len(all[qb]) != 1 || len(all[tr]) != 0 {
		t.Fatalf("after deletes = %+v", all)
	}
}

func TestWindowsRejectWhatTheScheduleCannotHold(t *testing.T) {
	d := newTestDB(t)
	qb := addTestDownloader(t, d, "qBittorrent")
	for name, w := range map[string]schedule.Window{
		"no days":   {Start: 0, End: 60},
		"late time": {Days: schedule.EveryDay, Start: 0, End: 1440},
	} {
		if _, err := d.CreateWindow(qb, w); err == nil {
			t.Errorf("%s: stored", name)
		}
	}
}

// A v1.1 database upgrades in place: its downloaders keep everything,
// gain the alternative speed setting switched off, and have no windows.
func TestOpenUpgradesAV11Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v11.db")
	conn, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	old := schemaSQL[:strings.Index(schemaSQL, "\n-- A downloader's time windows")]
	column := "    -- Throttle through the client's alternative speed limits, so its own\n    -- UI shows it. qBittorrent and Transmission only.\n    alternative_speed INTEGER NOT NULL DEFAULT 0,\n"
	if !strings.Contains(old, column) {
		t.Fatal("schema.sql's alternative_speed column changed; update this test")
	}
	old = strings.Replace(old, column, "", 1)
	for _, stmt := range []string{
		old,
		`INSERT INTO downloaders (id, name, type, url, credential_encrypted, default_download_limit, backup_upload_limit)
		 VALUES (1, 'Deluge', 'deluge', 'http://deluge', 'secret', 9000, 200)`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	_ = conn.Close()

	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	dl, err := d.GetDownloader(1)
	if err != nil {
		t.Fatal(err)
	}
	if dl.Name != "Deluge" || dl.CredentialEncrypted.String != "secret" || dl.DefaultDownloadLimit != 9000 || dl.BackupUploadLimit != 200 || dl.AlternativeSpeed || !dl.Enabled {
		t.Fatalf("downloader after the upgrade = %+v", dl)
	}
	windows, err := d.ListWindows()
	if err != nil || len(windows) != 0 {
		t.Fatalf("windows = %+v, %v", windows, err)
	}
	// Opening it again changes nothing.
	_ = d.Close()
	if d, err = Open(path); err != nil {
		t.Fatal(err)
	}
}
