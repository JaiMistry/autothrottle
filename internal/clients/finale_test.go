package clients

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestIsFinale(t *testing.T) {
	show := []episodeNumber{{0, 1}, {1, 1}, {1, 2}, {2, 1}, {2, 2}}
	cases := []struct {
		name    string
		current episodeNumber
		want    bool
	}{
		{"the show's last episode", episodeNumber{2, 2}, true},
		{"one with another after it in its season", episodeNumber{2, 1}, false},
		{"a season's last, with a season after it", episodeNumber{1, 2}, false},
		{"a special, which can't be placed", episodeNumber{0, 1}, false},
		{"an unnumbered episode", episodeNumber{2, 0}, false},
	}
	for _, c := range cases {
		if got := isFinale(c.current, show); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// fakePlexShow is a Plex server playing episode 1x2, in a show whose
// episodes are leaves, or whose lookup fails; asked counts the lookups.
func fakePlexShow(t *testing.T, leaves [][2]int, fail bool, asked *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "tok" {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		var body any
		switch r.URL.Path {
		case "/status/sessions":
			body = map[string]any{"MediaContainer": map[string]any{"Metadata": []map[string]any{{
				"sessionKey": "7", "ratingKey": "102", "grandparentRatingKey": "100",
				"type": "episode", "grandparentTitle": "Show", "parentIndex": 1, "index": 2,
				"duration": 1_800_000, "viewOffset": 60_000, "Player": map[string]any{"state": "playing"},
			}}}}
		case "/library/metadata/100/allLeaves":
			asked.Add(1)
			if fail {
				http.Error(w, "busy", http.StatusServiceUnavailable)
				return
			}
			var metadata []map[string]int
			for _, l := range leaves {
				metadata = append(metadata, map[string]int{"parentIndex": l[0], "index": l[1]})
			}
			body = map[string]any{"MediaContainer": map[string]any{"Metadata": metadata}}
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPlexKnowsAShowsLastEpisode(t *testing.T) {
	for _, c := range []struct {
		name   string
		leaves [][2]int
		want   bool
	}{
		{"nothing after it", [][2]int{{1, 1}, {1, 2}}, true},
		{"a next season", [][2]int{{1, 1}, {1, 2}, {2, 1}}, false},
	} {
		var asked atomic.Int32
		w := NewPlexWatcher(fakePlexShow(t, c.leaves, false, &asked).URL, "tok", false)
		for range 2 {
			sessions, err := w.Snapshot(context.Background())
			if err != nil || len(sessions) != 1 {
				t.Fatalf("%s: %+v, %v", c.name, sessions, err)
			}
			if s := sessions[0]; s.Finale != c.want || !s.Episode || s.ItemID != "102" {
				t.Errorf("%s: %+v", c.name, s)
			}
		}
		if asked.Load() != 1 {
			t.Errorf("%s: the show was looked up %d times, want once", c.name, asked.Load())
		}
	}
}

// A lookup that fails holds the throttle as for any episode, and is
// tried again on the next report.
func TestAFailedFinaleLookupIsAskedAgain(t *testing.T) {
	var asked atomic.Int32
	w := NewPlexWatcher(fakePlexShow(t, nil, true, &asked).URL, "tok", false)
	for range 2 {
		sessions, err := w.Snapshot(context.Background())
		if err != nil || len(sessions) != 1 || sessions[0].Finale || !sessions[0].Episode {
			t.Fatalf("%+v, %v", sessions, err)
		}
	}
	if asked.Load() != 2 {
		t.Errorf("a failed lookup was asked %d times, want again each time", asked.Load())
	}
}

// fakeJellyfinShow plays episode 1x2 of series s1 for user u1; Episodes
// lists the show, a Virtual one among them if virtual is set.
func fakeJellyfinShow(t *testing.T, episodes [][2]int, virtual bool, asked *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		switch r.URL.Path {
		case "/Sessions":
			body = []map[string]any{{
				"Id": "sess", "UserId": "u1", "DeviceName": "TV",
				"NowPlayingItem": map[string]any{"Id": "ep2", "SeriesId": "s1", "Type": "Episode",
					"SeriesName": "Show", "ParentIndexNumber": 1, "IndexNumber": 2, "RunTimeTicks": 18_000_000_000},
				"PlayState": map[string]any{"PositionTicks": 600_000_000},
			}}
		case "/Shows/s1/Episodes":
			asked.Add(1)
			if r.URL.Query().Get("UserId") != "u1" || r.URL.Query().Get("IsMissing") != "false" {
				http.Error(w, "missing parameters: "+r.URL.RawQuery, http.StatusBadRequest)
				return
			}
			var items []map[string]any
			for _, e := range episodes {
				items = append(items, map[string]any{"ParentIndexNumber": e[0], "IndexNumber": e[1], "LocationType": "FileSystem"})
			}
			if virtual {
				items = append(items, map[string]any{"ParentIndexNumber": 1, "IndexNumber": 3, "LocationType": "Virtual"})
			}
			body = map[string]any{"Items": items}
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestJellyfinAndEmbyKnowAShowsLastEpisode(t *testing.T) {
	for _, c := range []struct {
		name     string
		episodes [][2]int
		virtual  bool
		want     bool
	}{
		{"nothing after it", [][2]int{{1, 1}, {1, 2}}, false, true},
		{"only a listed episode not on disk after it", [][2]int{{1, 1}, {1, 2}}, true, true},
		{"one after it", [][2]int{{1, 1}, {1, 2}, {1, 3}}, false, false},
	} {
		for _, flavor := range []embyFlavor{flavorJellyfin, flavorEmby} {
			var asked atomic.Int32
			w := newEmbyWatcher(flavor, fakeJellyfinShow(t, c.episodes, c.virtual, &asked).URL, "key", false)
			for range 2 {
				sessions, err := w.Snapshot(context.Background())
				if err != nil || len(sessions) != 1 {
					t.Fatalf("%s %s: %+v, %v", flavor.name, c.name, sessions, err)
				}
				if s := sessions[0]; s.Finale != c.want || !s.Episode || s.ItemID != "ep2" {
					t.Errorf("%s %s: %+v", flavor.name, c.name, s)
				}
			}
			if asked.Load() != 1 {
				t.Errorf("%s %s: the show was looked up %d times, want once", flavor.name, c.name, asked.Load())
			}
		}
	}
}
