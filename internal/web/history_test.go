package web

import (
	"strings"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/db"
)

func at(day time.Time, h, m int) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, time.Local)
}

func TestHistoryCells(t *testing.T) {
	now := time.Date(2026, 10, 2, 21, 30, 0, 0, time.Local) // a Friday
	yesterday := now.AddDate(0, 0, -1)
	periods := []db.Span{
		{Start: at(yesterday, 20, 10), End: at(yesterday, 20, 30)}, // 20 min, one span
		{Start: at(yesterday, 20, 40), End: at(yesterday, 21, 50)}, // runs over the hour
		{Start: at(now, 21, 5)}, // still going
	}
	runs := []db.Span{{Start: now.AddDate(0, 0, -3), End: now}}
	v := buildHistory(periods, runs, now, 7)

	today, yday := v.Rows[0], v.Rows[1]
	if today.Label != "Fri 2" || !today.Today || yday.Label != "Thu 1" || v.Corner != "Oct" {
		t.Fatalf("rows labelled %q / %q, corner %q", today.Label, yday.Label, v.Corner)
	}

	c := yday.Cells[20]
	if !c.Hot || c.Step != 3 || c.Value != "40 min throttled" || c.Side != "r" {
		t.Errorf("Thu 20:00 = %+v", c)
	}
	if want := []string{"8:10 PM – 8:30 PM", "8:40 PM – 9:00 PM"}; strings.Join(c.Spans, "|") != strings.Join(want, "|") {
		t.Errorf("spans = %q, want each clipped to the hour", c.Spans)
	}
	if c.When != "Thu 1 Oct" || !strings.HasPrefix(c.Label, "40 min throttled · 8:10 PM") {
		t.Errorf("when %q, label %q", c.When, c.Label)
	}
	if yday.Cells[21].Step != 4 {
		t.Errorf("50 minutes is step %d, want 4", yday.Cells[21].Step)
	}

	live := today.Cells[21]
	if live.Value != "25 min throttled so far" || live.Spans[0] != "9:05 PM – now" {
		t.Errorf("the hour in progress = %+v", live)
	}
	if !today.Cells[22].Future || today.Cells[22].Label != "" {
		t.Errorf("a future hour = %+v", today.Cells[22])
	}
	if quiet := today.Cells[10]; quiet.Hot || quiet.Label != "Not throttled · Fri 2 Oct" {
		t.Errorf("a quiet hour = %+v", quiet)
	}
	// Three days ago, before the only run began: not running, not "not throttled".
	if off := v.Rows[4].Cells[12]; off.Hot || off.Step != 0 || !strings.HasPrefix(off.Label, "AutoThrottle was off") {
		t.Errorf("an hour the app was down = %+v", off)
	}

	if v.TotalText() != "1h 55m" || v.Empty || v.Total7 != 115 || v.Total30 != 115 {
		t.Errorf("total %q (7d %d, 30d %d), empty %v", v.TotalText(), v.Total7, v.Total30, v.Empty)
	}
}

func TestHistoryMonthBreakAndEmpty(t *testing.T) {
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.Local)
	v := buildHistory(nil, nil, now, 30)
	if len(v.Rows) != historyDays {
		t.Fatalf("%d rows, want %d", len(v.Rows), historyDays)
	}
	// Oct 2, Oct 1, then Sep 30 opens a new month.
	if v.Rows[1].MonthBreak || !v.Rows[2].MonthBreak || v.Rows[2].Month != "Sep" {
		t.Errorf("month breaks: %v %v (%q)", v.Rows[1].MonthBreak, v.Rows[2].MonthBreak, v.Rows[2].Month)
	}
	if !v.Empty || v.TotalText() != "0m" || !v.ShowM() || v.ShowH() {
		t.Errorf("empty history reads %q, empty %v", v.TotalText(), v.Empty)
	}
}

func TestTotalLabel(t *testing.T) {
	for minutes, want := range map[int]string{0: "0m", 45: "45m", 180: "3h", 1095: "18h 15m"} {
		if got := totalLabel(minutes); got != want {
			t.Errorf("totalLabel(%d) = %q, want %q", minutes, got, want)
		}
	}
}
