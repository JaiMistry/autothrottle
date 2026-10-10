package db

import (
	"testing"
	"time"
)

func TestThrottlePeriodsAndRuns(t *testing.T) {
	d := newTestDB(t)
	t0 := time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC)

	run, err := d.StartRun(t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.OpenThrottlePeriod(t0.Add(10 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := d.CloseThrottlePeriod(t0.Add(50 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := d.OpenThrottlePeriod(t0.Add(70 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := d.Heartbeat(run, t0.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}

	periods, err := d.ThrottlePeriods(t0, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(periods) != 2 || !periods[0].End.Equal(t0.Add(50*time.Minute)) || !periods[1].End.IsZero() {
		t.Fatalf("periods = %+v", periods)
	}
	if early, _ := d.ThrottlePeriods(t0, t0.Add(5*time.Minute)); len(early) != 0 {
		t.Errorf("a period outside the window was returned: %+v", early)
	}

	// A run that dies mid-period: the next start closes it, and the open
	// session, at the last heartbeat.
	if _, err := d.conn.Exec(`INSERT INTO session_history (media_server_id, session_key, started_at) VALUES (?, 's', ?)`,
		addTestMediaServer(t, d, "Plex"), FormatTime(t0.Add(80*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err := d.StartRun(t0.Add(5 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	periods, _ = d.ThrottlePeriods(t0, t0.Add(6*time.Hour))
	if !periods[1].End.Equal(t0.Add(90 * time.Minute)) {
		t.Errorf("dangling period ended at %s, want the last heartbeat", periods[1].End)
	}
	if open := allSessions(t, d); open[0].EndedAt.String != FormatTime(t0.Add(90*time.Minute)) {
		t.Errorf("dangling session ended at %q, want the last heartbeat", open[0].EndedAt.String)
	}

	runs, err := d.Runs(t0, t0.Add(6*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || !runs[0].End.Equal(t0.Add(90*time.Minute)) || !runs[1].Start.Equal(t0.Add(5*time.Hour)) {
		t.Fatalf("runs = %+v", runs)
	}

	if n, err := d.PruneHistory(t0.Add(3 * time.Hour)); err != nil || n != 3 {
		t.Errorf("pruned %d rows (%v), want the first run and both finished periods", n, err)
	}
}

// With no earlier run to bound it, a dangling period gets no duration.
func TestDanglingPeriodWithoutAnEarlierRun(t *testing.T) {
	d := newTestDB(t)
	start := time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC)
	if err := d.OpenThrottlePeriod(start); err != nil {
		t.Fatal(err)
	}
	if _, err := d.StartRun(start.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	periods, _ := d.ThrottlePeriods(start.Add(-time.Hour), start.Add(2*time.Hour))
	if len(periods) != 1 || !periods[0].End.Equal(start) {
		t.Errorf("periods = %+v, want one ending where it began", periods)
	}
}
