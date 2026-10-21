// Package schedule decides which of a downloader's time windows is in
// force at a given moment. Times are wall-clock, in the location of the
// time passed in (the process's TZ), so a window keeps its hours across a
// daylight-saving change.
package schedule

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Days is a set of weekdays, bit n being time.Weekday(n): Sunday is bit 0.
type Days uint8

const (
	EveryDay Days = 1<<7 - 1
	Weekends Days = 1<<time.Sunday | 1<<time.Saturday
	Weekdays Days = EveryDay &^ Weekends
)

// Has reports whether the set includes day.
func (d Days) Has(day time.Weekday) bool { return d&(1<<day) != 0 }

// String names the set the way the page shows it.
func (d Days) String() string {
	switch d & EveryDay {
	case EveryDay:
		return "Every day"
	case Weekdays:
		return "Weekdays"
	case Weekends:
		return "Weekends"
	case 0:
		return "No days"
	}
	// Monday first, as a week reads.
	var names []string
	for i := 1; i <= 7; i++ {
		day := time.Weekday(i % 7)
		if d.Has(day) {
			names = append(names, day.String()[:3])
		}
	}
	return strings.Join(names, ", ")
}

const (
	minutesPerDay  = 24 * 60
	minutesPerWeek = 7 * minutesPerDay
)

// Window is a span of time on some days with its own rates, which
// replace the downloader's Default rates while it lasts.
type Window struct {
	ID   int64
	Name string
	Days Days
	// Start and End are minutes after midnight. An End at or before Start
	// runs past midnight and belongs to the day it starts on; an End equal
	// to Start lasts the whole day.
	Start, End int
	// DownloadKBps and UploadKBps are the window's rates; 0 is unlimited.
	DownloadKBps, UploadKBps int64
	// Throttle is whether playback still throttles during the window.
	Throttle bool
}

// Validate reports a window that can't be saved.
func (w Window) Validate() error {
	switch {
	case w.Days&EveryDay == 0:
		return errors.New("choose at least one day")
	case w.Start < 0 || w.Start >= minutesPerDay || w.End < 0 || w.End >= minutesPerDay:
		return errors.New("times must be between 00:00 and 23:59")
	case w.DownloadKBps < 0 || w.UploadKBps < 0:
		return errors.New("rates can't be negative")
	}
	return nil
}

// length is how many minutes the window lasts.
func (w Window) length() int {
	if l := (w.End - w.Start + minutesPerDay) % minutesPerDay; l > 0 {
		return l
	}
	return minutesPerDay
}

// Label is the window's name, or its hours when it has none.
func (w Window) Label() string {
	if w.Name != "" {
		return w.Name
	}
	return Clock(w.Start) + "–" + Clock(w.End)
}

// Clock formats minutes after midnight as HH:MM.
func Clock(minutes int) string {
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}

// startedOn returns the midnight of the day whose occurrence of w covers
// t, and whether one does.
func (w Window) startedOn(t time.Time) (time.Time, bool) {
	minute := t.Hour()*60 + t.Minute()
	today := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	if w.Days.Has(t.Weekday()) && minute >= w.Start && minute < w.Start+w.length() {
		return today, true
	}
	// Yesterday's occurrence, if it runs past midnight into today.
	if spill := w.Start + w.length() - minutesPerDay; spill > 0 && minute < spill {
		yesterday := today.AddDate(0, 0, -1)
		if w.Days.Has(yesterday.Weekday()) {
			return yesterday, true
		}
	}
	return time.Time{}, false
}

// Covers reports whether the window is in force at t.
func (w Window) Covers(t time.Time) bool {
	_, ok := w.startedOn(t)
	return ok
}

// EndsAfter is when the occurrence of w covering t ends, for "until
// 07:00"; the zero time if w doesn't cover t.
func (w Window) EndsAfter(t time.Time) time.Time {
	day, ok := w.startedOn(t)
	if !ok {
		return time.Time{}
	}
	return time.Date(day.Year(), day.Month(), day.Day(), 0, w.Start+w.length(), 0, 0, t.Location())
}

// Active returns the window in force at t. Saved windows never overlap,
// so there is at most one; were there more, the first wins.
func Active(windows []Window, t time.Time) (Window, bool) {
	for _, w := range windows {
		if w.Covers(t) {
			return w, true
		}
	}
	return Window{}, false
}

// NextChange is the first moment after t at which any window starts or
// ends: when what's in force may next change. ok is false with no
// windows.
func NextChange(windows []Window, t time.Time) (next time.Time, ok bool) {
	today := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	// From yesterday, whose window may still be running, to a week ahead.
	for offset := -1; offset <= 7; offset++ {
		day := today.AddDate(0, 0, offset)
		for _, w := range windows {
			if !w.Days.Has(day.Weekday()) {
				continue
			}
			start := time.Date(day.Year(), day.Month(), day.Day(), 0, w.Start, 0, 0, t.Location())
			end := time.Date(day.Year(), day.Month(), day.Day(), 0, w.Start+w.length(), 0, 0, t.Location())
			for _, edge := range []time.Time{start, end} {
				if edge.After(t) && (!ok || edge.Before(next)) {
					next, ok = edge, true
				}
			}
		}
	}
	return next, ok
}

// Overlap finds two windows in force at the same minute of the week,
// returning their indexes; ok is false when none overlap.
func Overlap(windows []Window) (first, second int, ok bool) {
	var owner [minutesPerWeek]int // index+1 of the window holding each minute
	for i, w := range windows {
		for day := time.Sunday; day <= time.Saturday; day++ {
			if !w.Days.Has(day) {
				continue
			}
			from := int(day)*minutesPerDay + w.Start
			for m := 0; m < w.length(); m++ {
				slot := (from + m) % minutesPerWeek
				if owner[slot] != 0 && owner[slot] != i+1 {
					return owner[slot] - 1, i, true
				}
				owner[slot] = i + 1
			}
		}
	}
	return 0, 0, false
}
