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

	// The fill and the headline are the hour's own 40 minutes; a period
	// running past the hour is named whole beneath.
	c := yday.Cells[20]
	if !c.Hot || c.Step != 3 || c.Value != "40 min throttled" || c.Side != "r" {
		t.Errorf("Thu 20:00 = %+v", c)
	}
	if want := []string{"8:10 – 8:30 PM", "Part of 1hr 10m · 8:40 – 9:50 PM"}; strings.Join(c.Spans, "|") != strings.Join(want, "|") {
		t.Errorf("spans = %q, want %q", c.Spans, want)
	}
	if c.When != "Thu 1 Oct" || !strings.HasPrefix(c.Label, "40 min throttled · 8:10 – 8:30 PM") {
		t.Errorf("when %q, label %q", c.When, c.Label)
	}
	if next := yday.Cells[21]; next.Step != 4 || next.Value != "50 min throttled" || next.Spans[0] != "Part of 1hr 10m · 8:40 – 9:50 PM" {
		t.Errorf("Thu 21:00 = %+v", next)
	}

	live := today.Cells[21]
	if live.Value != "25 min throttled so far" || live.Spans[0] != "9:05 PM – now" {
		t.Errorf("the hour in progress = %+v", live)
	}
	// Still going from an earlier hour: the whole so far, beneath.
	ongoing := buildHistory([]db.Span{{Start: at(now, 20, 5)}}, runs, now, 7).Rows[0]
	if c := ongoing.Cells[21]; c.Value != "30 min throttled so far" || c.Spans[0] != "Part of 1hr 25m so far · 8:05 PM – now" {
		t.Errorf("an hour of a stretch still going = %+v", c)
	}
	if c := ongoing.Cells[20]; c.Value != "55 min throttled" {
		t.Errorf("a finished hour of a stretch still going = %+v", c)
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

	if v.TotalText() != "1hr 55m" || v.Empty || v.Total7 != 115 || v.Total30 != 115 {
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
	if !v.Empty || v.TotalText() != "0hr 0m" {
		t.Errorf("empty history reads %q, empty %v", v.TotalText(), v.Empty)
	}
}

func TestTotalLabel(t *testing.T) {
	for minutes, want := range map[int]string{0: "0hr 0m", 45: "0hr 45m", 180: "3hr 0m", 1095: "18hr 15m"} {
		if got := totalLabel(minutes); got != want {
			t.Errorf("totalLabel(%d) = %q, want %q", minutes, got, want)
		}
	}
	for minutes, want := range map[int]string{1: "1 min", 59: "59 min", 60: "1hr 0m", 155: "2hr 35m"} {
		if got := throttledLabel(minutes); got != want {
			t.Errorf("throttledLabel(%d) = %q, want %q", minutes, got, want)
		}
	}
}

// Throttling that stops and starts within a few minutes reads as one stretch.
func TestHistoryMergesFragments(t *testing.T) {
	now := time.Date(2026, 10, 9, 23, 0, 0, 0, time.Local)
	sec := func(h, m, s int) time.Time { return time.Date(2026, 10, 9, h, m, s, 0, time.Local) }
	periods := []db.Span{
		{Start: sec(20, 42, 5), End: sec(20, 42, 40)},
		{Start: sec(20, 42, 50), End: sec(20, 42, 58)},
		{Start: sec(20, 43, 10), End: sec(20, 44, 0)},
		{Start: sec(20, 44, 5), End: sec(20, 44, 30)},
		{Start: sec(20, 44, 40), End: sec(20, 50, 20)},
		{Start: sec(20, 58, 0), End: sec(20, 58, 20)},
	}
	runs := []db.Span{{Start: now.Add(-24 * time.Hour), End: now}}
	c := buildHistory(periods, runs, now, 7).Rows[0].Cells[20]
	if want := []string{"8:42 – 8:50 PM", "8:58 PM"}; strings.Join(c.Spans, "|") != strings.Join(want, "|") {
		t.Errorf("spans = %q, want %q", c.Spans, want)
	}
	// The minutes are the time throttled, not the gaps between.
	if c.Value != "8 min throttled" {
		t.Errorf("value = %q", c.Value)
	}

	// However busy the hour, the list stays short.
	var many []db.Span
	for m := 0; m < 60; m += 10 {
		many = append(many, db.Span{Start: sec(21, m, 0), End: sec(21, m+2, 0)})
	}
	if c := buildHistory(many, runs, now, 7).Rows[0].Cells[21]; len(c.Spans) != 4 || c.Spans[3] != "and 3 more" {
		t.Errorf("a busy hour lists %q", c.Spans)
	}
}

func TestClockRange(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 10, 9, h, m, 0, 0, time.Local) }
	for _, c := range []struct {
		from, to time.Time
		ongoing  bool
		want     string
	}{
		{at(20, 42), at(20, 50), false, "8:42 – 8:50 PM"},
		{at(11, 40), at(12, 20), false, "11:40 AM – 12:20 PM"},
		{at(20, 42), at(20, 42), false, "8:42 PM"},
		{at(21, 5), at(21, 30), true, "9:05 PM – now"},
	} {
		if got := clockRange(c.from, c.to, c.ongoing); got != c.want {
			t.Errorf("clockRange(%v, %v) = %q, want %q", c.from, c.to, got, c.want)
		}
	}
}
