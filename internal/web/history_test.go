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

	// The fill is the hour's own 40 minutes; the tooltip is the two
	// stretches through it (10 minutes apart), whole.
	c := yday.Cells[20]
	if !c.Hot || c.Step != 3 || c.Value != "1hr 30m throttled" || c.Side != "r" || c.Stretch != "0 1" {
		t.Errorf("Thu 20:00 = %+v", c)
	}
	if want := []string{"Thu 1 Oct · 8:10 – 8:30 PM", "Thu 1 Oct · 8:40 – 9:50 PM"}; strings.Join(c.Spans, "|") != strings.Join(want, "|") {
		t.Errorf("spans = %q, want %q", c.Spans, want)
	}
	if !strings.HasPrefix(c.Label, "1hr 30m throttled · Thu 1 Oct · 8:10 – 8:30 PM") {
		t.Errorf("label %q", c.Label)
	}
	if next := yday.Cells[21]; next.Step != 4 || next.Value != "1hr 10m throttled" || next.Stretch != "1" {
		t.Errorf("Thu 21:00 = %+v", next)
	}

	live := today.Cells[21]
	if live.Value != "25 min throttled so far" || live.Stretch != "2" || live.Spans[0] != "Fri 2 Oct · 9:05 PM – now" {
		t.Errorf("the hour in progress = %+v", live)
	}
	// Still going from an earlier hour: every hour of it says so.
	ongoing := buildHistory([]db.Span{{Start: at(now, 20, 5)}}, runs, now, 7).Rows[0]
	for _, hr := range []int{20, 21} {
		if c := ongoing.Cells[hr]; c.Value != "1hr 25m throttled so far" || c.Stretch != "0" || c.Spans[0] != "Fri 2 Oct · 8:05 PM – now" {
			t.Errorf("%d:00 of a stretch still going = %+v", hr, c)
		}
	}
	if c := ongoing.Cells[20]; c.Step != 4 {
		t.Errorf("its first hour is filled for 55 minutes: step %d", c.Step)
	}
	// Past midnight, both days are named.
	late := buildHistory([]db.Span{{Start: at(yesterday, 23, 40), End: at(now, 1, 15)}}, runs, now, 7)
	if c := late.Rows[0].Cells[0]; c.Spans[0] != "Thu 1 Oct, 11:40 PM – Fri 2 Oct, 1:15 AM" || c.Value != "1hr 35m throttled" {
		t.Errorf("a stretch past midnight = %+v", c)
	}
	if !today.Cells[22].Future || today.Cells[22].Label != "" {
		t.Errorf("a future hour = %+v", today.Cells[22])
	}
	if quiet := today.Cells[10]; quiet.Hot || quiet.Label != "Not throttled · Fri 2 Oct · 10–11 AM" {
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
	if want := []string{"Fri 9 Oct · 8:42 – 8:50 PM", "Fri 9 Oct · 8:58 PM"}; c.Stretch != "0 1" || strings.Join(c.Spans, "|") != strings.Join(want, "|") {
		t.Errorf("stretches %q, spans %q, want %q", c.Stretch, c.Spans, want)
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

func TestHourRange(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 10, 9, h, 0, 0, 0, time.Local) }
	for _, c := range []struct {
		hour int
		want string
	}{
		{20, "8–9 PM"},
		{0, "12–1 AM"},
		{11, "11 AM–12 PM"},
		{23, "11 PM–12 AM"},
	} {
		if got := hourRange(at(c.hour), at(c.hour+1)); got != c.want {
			t.Errorf("hourRange(%d) = %q, want %q", c.hour, got, c.want)
		}
	}
}
