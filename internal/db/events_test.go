package db

import (
	"database/sql"
	"testing"
	"time"
)

func insertEvent(t *testing.T, d *DB, e Event) {
	t.Helper()
	if err := d.InsertEvent(e); err != nil {
		t.Fatalf("inserting event: %v", err)
	}
}

func TestListEventsFiltered(t *testing.T) {
	d := newTestDB(t)
	dlID := addTestDownloader(t, d, "qb")
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	insertEvent(t, d, Event{OccurredAt: FormatTime(base), Level: "info", Message: "throttled qb because Plex is playing",
		DownloaderID: sql.NullInt64{Int64: dlID, Valid: true}})
	insertEvent(t, d, Event{OccurredAt: FormatTime(base.Add(time.Minute)), Level: "error", Message: "failed to apply rate limits to qb"})
	insertEvent(t, d, Event{OccurredAt: FormatTime(base.Add(2 * time.Hour)), Level: "info", Message: "restored default rate for qb"})

	t.Run("newest first", func(t *testing.T) {
		events, err := d.ListEventsFiltered(EventFilter{})
		if err != nil {
			t.Fatalf("listing: %v", err)
		}
		if len(events) != 3 {
			t.Fatalf("got %d events, want 3", len(events))
		}
		if events[0].Message != "restored default rate for qb" {
			t.Errorf("first event = %q, want the newest", events[0].Message)
		}
	})

	t.Run("by level", func(t *testing.T) {
		events, err := d.ListEventsFiltered(EventFilter{Levels: []string{"error"}})
		if err != nil {
			t.Fatalf("listing: %v", err)
		}
		if len(events) != 1 || events[0].Level != "error" {
			t.Fatalf("got %+v, want the single error event", events)
		}
	})

	t.Run("by downloader", func(t *testing.T) {
		events, err := d.ListEventsFiltered(EventFilter{DownloaderID: dlID})
		if err != nil {
			t.Fatalf("listing: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("got %d events, want the 1 attributed to the downloader", len(events))
		}
	})

	t.Run("by time window", func(t *testing.T) {
		events, err := d.ListEventsFiltered(EventFilter{Since: base.Add(time.Hour)})
		if err != nil {
			t.Fatalf("listing: %v", err)
		}
		if len(events) != 1 || events[0].Message != "restored default rate for qb" {
			t.Fatalf("got %+v, want only the event after the cutoff", events)
		}
	})

	t.Run("by message search", func(t *testing.T) {
		events, err := d.ListEventsFiltered(EventFilter{Search: "THROTTLED"})
		if err != nil {
			t.Fatalf("listing: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("got %d events, want 1 case-insensitive match", len(events))
		}
	})

	t.Run("limit and offset", func(t *testing.T) {
		events, err := d.ListEventsFiltered(EventFilter{Limit: 1, Offset: 1})
		if err != nil {
			t.Fatalf("listing: %v", err)
		}
		if len(events) != 1 || events[0].Level != "error" {
			t.Fatalf("got %+v, want the second-newest event", events)
		}
	})

	t.Run("count ignores paging", func(t *testing.T) {
		n, err := d.CountEvents(EventFilter{Limit: 1})
		if err != nil {
			t.Fatalf("counting: %v", err)
		}
		if n != 3 {
			t.Fatalf("count = %d, want 3", n)
		}
	})
}

// A search string containing LIKE wildcards must be matched literally,
// not treated as a pattern — otherwise searching "100%" returns
// everything.
func TestListEventsFilteredEscapesSearchWildcards(t *testing.T) {
	d := newTestDB(t)
	insertEvent(t, d, Event{Level: "info", Message: "disk usage at 100% on media volume"})
	insertEvent(t, d, Event{Level: "info", Message: "nothing to report"})

	events, err := d.ListEventsFiltered(EventFilter{Search: "100%"})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want only the literal match", len(events))
	}
}

func TestPruneEvents(t *testing.T) {
	d := newTestDB(t)
	insertEvent(t, d, Event{OccurredAt: FormatTime(time.Now().Add(-100 * 24 * time.Hour)), Level: "info", Message: "ancient"})
	insertEvent(t, d, Event{Level: "info", Message: "fresh"})

	deleted, err := d.PruneEvents(time.Now().Add(-90 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("pruning: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}

	events, err := d.ListEvents(10)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(events) != 1 || events[0].Message != "fresh" {
		t.Fatalf("remaining = %+v, want only the fresh event", events)
	}
}
