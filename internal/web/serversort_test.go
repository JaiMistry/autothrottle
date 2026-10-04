package web

import (
	"slices"
	"testing"
)

func TestPlayingServersSortToTopStably(t *testing.T) {
	rows := []serverRowView{
		{Name: "Plex", State: "idle"},
		{Name: "Emby", State: "playing", Sessions: 1},
		{Name: "Kodi", State: "unreachable"},
		{Name: "Jellyfin", State: "playing", Sessions: 1},
	}
	want := []string{"Emby", "Jellyfin", "Plex", "Kodi"}
	names := func() []string {
		var out []string
		for _, r := range rows {
			out = append(out, r.Name)
		}
		return out
	}

	sortServers(rows)
	if !slices.Equal(names(), want) {
		t.Fatalf("got %v, want %v", names(), want)
	}
	// A refresh, or a second stream on a playing server, moves nothing.
	rows[1].Sessions = 4
	sortServers(rows)
	if !slices.Equal(names(), want) {
		t.Errorf("a re-sort reordered the list: %v", names())
	}
}
