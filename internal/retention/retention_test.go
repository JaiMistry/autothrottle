package retention

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/db"
)

// The steps are independent by design, so the risk worth covering is a
// pass that quietly stops after the first.
func TestRunOncePrunesEverything(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	defer func() { _ = database.Close() }()

	now := time.Now()
	ancient := now.Add(-100 * 24 * time.Hour)
	if err := database.InsertEvent(db.Event{OccurredAt: db.FormatTime(ancient), Level: "info", Message: "ancient"}); err != nil {
		t.Fatal(err)
	}
	if err := database.InsertEvent(db.Event{Level: "info", Message: "fresh"}); err != nil {
		t.Fatal(err)
	}
	if err := database.OpenThrottlePeriod(ancient); err != nil {
		t.Fatal(err)
	}
	if err := database.CloseThrottlePeriod(ancient.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := database.OpenThrottlePeriod(now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	if err := database.CreateAuthSession("expired", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	ms, err := database.CreateMediaServer(db.MediaServer{Name: "Plex", Type: "plex", URL: "http://plex.lan"})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.OpenSession(ms, "old", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := database.CloseSession(ms, "old"); err != nil {
		t.Fatal(err)
	}
	// Ended long ago, as only time can do otherwise.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`UPDATE session_history SET started_at = ?, ended_at = ?`, db.FormatTime(ancient), db.FormatTime(ancient)); err != nil {
		t.Fatal(err)
	}

	New(database).RunOnce()

	events, err := database.ListEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Message != "fresh" {
		t.Errorf("remaining events = %+v, want only the fresh one", events)
	}
	periods, err := database.ThrottlePeriods(ancient.Add(-time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(periods) != 1 || !periods[0].End.IsZero() {
		t.Errorf("remaining throttle periods = %+v, want only the open one", periods)
	}
	var sessions int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM session_history`).Scan(&sessions); err != nil || sessions != 0 {
		t.Errorf("%d sessions left, %v", sessions, err)
	}
}

// A failed step does not stop the pass, and Run stops with its context.
func TestRunSurvivesAFailedDatabase(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = database.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	New(database).Run(ctx)
}
