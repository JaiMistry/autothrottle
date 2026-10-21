package schedule

import (
	"testing"
	"time"
)

// 2026-10-19 is a Monday.
func at(day, hour, minute int) time.Time {
	return time.Date(2026, 10, day, hour, minute, 0, 0, time.UTC)
}

func hm(hour, minute int) int { return hour*60 + minute }

func TestDaysString(t *testing.T) {
	for _, tc := range []struct {
		days Days
		want string
	}{
		{EveryDay, "Every day"},
		{Weekdays, "Weekdays"},
		{Weekends, "Weekends"},
		{1<<time.Monday | 1<<time.Wednesday | 1<<time.Sunday, "Mon, Wed, Sun"},
		{0, "No days"},
	} {
		if got := tc.days.String(); got != tc.want {
			t.Errorf("%07b: got %q, want %q", tc.days, got, tc.want)
		}
	}
}

func TestCovers(t *testing.T) {
	night := Window{Days: EveryDay, Start: hm(23, 0), End: hm(7, 0)}
	work := Window{Days: Weekdays, Start: hm(9, 0), End: hm(17, 0)}
	saturdayNight := Window{Days: 1 << time.Saturday, Start: hm(22, 0), End: hm(2, 0)}
	allMonday := Window{Days: 1 << time.Monday, Start: 0, End: 0}
	mondayFromSeven := Window{Days: 1 << time.Monday, Start: hm(7, 0), End: hm(7, 0)}

	for _, tc := range []struct {
		name string
		w    Window
		t    time.Time
		want bool
	}{
		{"night, before it", night, at(19, 22, 59), false},
		{"night, its first minute", night, at(19, 23, 0), true},
		{"night, past midnight", night, at(20, 3, 0), true},
		{"night, its last minute", night, at(20, 6, 59), true},
		{"night, as it ends", night, at(20, 7, 0), false},
		{"work, on a weekday", work, at(21, 12, 0), true},
		{"work, on a Saturday", work, at(24, 12, 0), false},
		{"work, at its end", work, at(21, 17, 0), false},
		{"Saturday night, Saturday", saturdayNight, at(24, 23, 0), true},
		{"Saturday night, into Sunday", saturdayNight, at(25, 1, 30), true},
		{"Saturday night, Sunday evening", saturdayNight, at(25, 23, 0), false},
		{"Saturday night, Friday into Saturday", saturdayNight, at(24, 1, 0), false},
		{"all Monday, midnight", allMonday, at(19, 0, 0), true},
		{"all Monday, last minute", allMonday, at(19, 23, 59), true},
		{"all Monday, Tuesday", allMonday, at(20, 0, 0), false},
		{"Monday from seven, Tuesday morning", mondayFromSeven, at(20, 6, 59), true},
		{"Monday from seven, Monday morning", mondayFromSeven, at(19, 6, 59), false},
	} {
		if got := tc.w.Covers(tc.t); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestEndsAfter(t *testing.T) {
	night := Window{Days: EveryDay, Start: hm(23, 0), End: hm(7, 0)}
	if got, want := night.EndsAfter(at(19, 23, 30)), at(20, 7, 0); !got.Equal(want) {
		t.Errorf("before midnight: got %v, want %v", got, want)
	}
	if got, want := night.EndsAfter(at(20, 2, 0)), at(20, 7, 0); !got.Equal(want) {
		t.Errorf("after midnight: got %v, want %v", got, want)
	}
	if got := night.EndsAfter(at(20, 12, 0)); !got.IsZero() {
		t.Errorf("outside: got %v, want zero", got)
	}
}

func TestActive(t *testing.T) {
	windows := []Window{
		{ID: 1, Days: EveryDay, Start: hm(0, 0), End: hm(7, 0)},
		{ID: 2, Days: Weekdays, Start: hm(9, 0), End: hm(17, 0)},
	}
	if w, ok := Active(windows, at(21, 10, 0)); !ok || w.ID != 2 {
		t.Errorf("weekday morning: got %v %v, want window 2", w.ID, ok)
	}
	if _, ok := Active(windows, at(21, 8, 0)); ok {
		t.Error("between windows: want none")
	}
	if _, ok := Active(nil, at(21, 8, 0)); ok {
		t.Error("no windows: want none")
	}
}

func TestNextChange(t *testing.T) {
	windows := []Window{
		{Days: EveryDay, Start: hm(23, 0), End: hm(7, 0)},
		{Days: Weekdays, Start: hm(9, 0), End: hm(17, 0)},
	}
	for _, tc := range []struct {
		name     string
		from     time.Time
		want     time.Time
		windowed []Window
	}{
		{"inside the night, its end", at(20, 3, 0), at(20, 7, 0), windows},
		{"between, the next start", at(20, 8, 0), at(20, 9, 0), windows},
		{"at a boundary, the one after", at(20, 9, 0), at(20, 17, 0), windows},
		{"Friday evening, the night", at(23, 18, 0), at(23, 23, 0), windows},
		{"one weekly window, a week on", at(19, 12, 0), at(26, 9, 0),
			[]Window{{Days: 1 << time.Monday, Start: hm(9, 0), End: hm(10, 0)}}},
	} {
		got, ok := NextChange(tc.windowed, tc.from)
		if !ok || !got.Equal(tc.want) {
			t.Errorf("%s: got %v %v, want %v", tc.name, got, ok, tc.want)
		}
	}
	if _, ok := NextChange(nil, at(20, 3, 0)); ok {
		t.Error("no windows: want no change")
	}
}

// A window keeps its wall-clock hours across a daylight-saving change.
func TestDaylightSaving(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no time zone database:", err)
	}
	// Clocks went back an hour at 02:00 on Sunday 2026-11-01.
	night := Window{Days: EveryDay, Start: hm(23, 0), End: hm(7, 0)}
	from := time.Date(2026, 10, 31, 23, 30, 0, 0, ny)
	end := night.EndsAfter(from)
	if want := time.Date(2026, 11, 1, 7, 0, 0, 0, ny); !end.Equal(want) {
		t.Errorf("end: got %v, want %v", end, want)
	}
	// Nine hours of elapsed time for an eight-hour window that night.
	if got := end.Sub(time.Date(2026, 10, 31, 23, 0, 0, 0, ny)); got != 9*time.Hour {
		t.Errorf("elapsed: got %v, want 9h", got)
	}
	next, ok := NextChange([]Window{night}, from)
	if !ok || !next.Equal(end) {
		t.Errorf("next change: got %v, want %v", next, end)
	}
}

func TestOverlap(t *testing.T) {
	for _, tc := range []struct {
		name    string
		windows []Window
		want    bool
	}{
		{"apart", []Window{
			{Days: EveryDay, Start: hm(0, 0), End: hm(7, 0)},
			{Days: EveryDay, Start: hm(7, 0), End: hm(9, 0)},
		}, false},
		{"same hours, different days", []Window{
			{Days: Weekdays, Start: hm(9, 0), End: hm(17, 0)},
			{Days: Weekends, Start: hm(9, 0), End: hm(17, 0)},
		}, false},
		{"one inside another", []Window{
			{Days: EveryDay, Start: hm(9, 0), End: hm(17, 0)},
			{Days: 1 << time.Tuesday, Start: hm(12, 0), End: hm(13, 0)},
		}, true},
		{"overnight into the next morning's window", []Window{
			{Days: 1 << time.Monday, Start: hm(22, 0), End: hm(8, 0)},
			{Days: 1 << time.Tuesday, Start: hm(7, 0), End: hm(9, 0)},
		}, true},
		{"Saturday night into Sunday morning", []Window{
			{Days: 1 << time.Saturday, Start: hm(23, 0), End: hm(2, 0)},
			{Days: 1 << time.Sunday, Start: hm(1, 0), End: hm(3, 0)},
		}, true},
		{"a whole day and anything that day", []Window{
			{Days: 1 << time.Monday, Start: 0, End: 0},
			{Days: 1 << time.Monday, Start: hm(23, 59), End: 0},
		}, true},
	} {
		if _, _, got := Overlap(tc.windows); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestValidate(t *testing.T) {
	good := Window{Days: Weekdays, Start: hm(9, 0), End: hm(17, 0)}
	if err := good.Validate(); err != nil {
		t.Errorf("good window: %v", err)
	}
	for name, w := range map[string]Window{
		"no days":       {Start: 0, End: 60},
		"past midnight": {Days: EveryDay, Start: 0, End: minutesPerDay},
		"negative rate": {Days: EveryDay, Start: 0, End: 60, DownloadKBps: -1},
	} {
		if w.Validate() == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestLabel(t *testing.T) {
	if got := (Window{Start: hm(23, 0), End: hm(7, 5)}).Label(); got != "23:00–07:05" {
		t.Errorf("unnamed: got %q", got)
	}
	if got := (Window{Name: "Night", Start: 0, End: 60}).Label(); got != "Night" {
		t.Errorf("named: got %q", got)
	}
}

func TestCheck(t *testing.T) {
	night := Window{Name: "Night", Days: EveryDay, Start: hm(23, 0), End: hm(7, 0)}
	early := Window{Days: Weekdays, Start: hm(6, 0), End: hm(9, 0)}
	if err := Check([]Window{night, {Days: Weekdays, Start: hm(7, 0), End: hm(9, 0)}}); err != nil {
		t.Errorf("apart: %v", err)
	}
	if err := Check([]Window{night, early}); err == nil || err.Error() != "Night and 06:00–09:00 overlap; a time can only be in one window" {
		t.Errorf("overlapping: %v", err)
	}
	if err := Check([]Window{{Name: "Empty", Start: 0, End: 60}}); err == nil || err.Error() != "Empty: choose at least one day" {
		t.Errorf("invalid: %v", err)
	}
}
