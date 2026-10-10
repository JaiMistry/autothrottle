package db

import "testing"

// The Logs page refreshes on this hook, so every write and the clear fire it.
func TestEventsChangedHook(t *testing.T) {
	d := newTestDB(t)
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
