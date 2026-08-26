package web

import (
	"sort"
	"testing"
)

// The ordering rule the Dashboard applies: playing first, configured
// order preserved inside each group.
func sortServers(rows []serverRowView) {
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].State == "playing" && rows[j].State != "playing"
	})
}

func names(rows []serverRowView) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Name
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPlayingServersSortToTop(t *testing.T) {
	rows := []serverRowView{
		{Name: "Plex", State: "idle"},
		{Name: "Emby", State: "idle"},
		{Name: "Jellyfin", State: "playing"},
	}
	sortServers(rows)
	if want := []string{"Jellyfin", "Plex", "Emby"}; !equal(names(rows), want) {
		t.Errorf("got %v, want %v", names(rows), want)
	}
}

// Nothing may move on a refresh that changed nothing — that is what
// stops the list shuffling under the pointer every few seconds.
func TestOrderIsStableWhenNothingChanged(t *testing.T) {
	rows := []serverRowView{
		{Name: "Plex", State: "playing"},
		{Name: "Emby", State: "idle"},
		{Name: "Jellyfin", State: "playing"},
		{Name: "Kodi", State: "unreachable"},
	}
	sortServers(rows)
	first := names(rows)
	for i := 0; i < 5; i++ {
		sortServers(rows)
		if !equal(names(rows), first) {
			t.Fatalf("order changed on a repeat sort: %v then %v", first, names(rows))
		}
	}
	if want := []string{"Plex", "Jellyfin", "Emby", "Kodi"}; !equal(first, want) {
		t.Errorf("got %v, want %v", first, want)
	}
}

// A second stream starting on an already-playing server must not
// reorder anything: the key is whether a server plays, not how much.
func TestSecondSessionDoesNotReorder(t *testing.T) {
	rows := []serverRowView{
		{Name: "Plex", State: "playing", Sessions: 1},
		{Name: "Jellyfin", State: "playing", Sessions: 1},
		{Name: "Emby", State: "idle"},
	}
	sortServers(rows)
	before := names(rows)

	rows[1].Sessions = 4
	sortServers(rows)
	if !equal(names(rows), before) {
		t.Errorf("a second session reordered the list: %v then %v", before, names(rows))
	}
}
