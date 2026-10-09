package db

import (
	"path/filepath"
	"testing"
)

// The Logs page refreshes when the log changes, so every write and the
// clear have to say so.
func TestEventsChangedHook(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if err := d.InsertEvent(Event{Level: "info", Message: "before any hook"}); err != nil {
		t.Fatal(err)
	}
	n := 0
	d.OnEventsChanged(func() { n++ })
	if err := d.InsertEvent(Event{Level: "info", Message: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DeleteAllEvents(); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("hook ran %d times, want 2", n)
	}
}
