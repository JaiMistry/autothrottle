package clients

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEmbyStateCounts(t *testing.T) {
	playing := embySession{ID: "1"}
	playing.NowPlayingItem = &embyNowPlaying{Name: "Arrival"}

	paused := playing
	paused.PlayState.IsPaused = true

	idle := embySession{ID: "2"} // connected client, nothing loaded

	if !embyStateCounts(playing) {
		t.Error("a session with media loaded should count")
	}
	// A pause does not end a stream. How long it goes on counting is
	// pausedTracker's job, not this rule's.
	if !embyStateCounts(paused) {
		t.Error("a paused session still has media loaded, so it should count")
	}
	if embyStateCounts(idle) {
		t.Error("a session with no NowPlayingItem should not count")
	}
}

func TestEmbySessionIsLocal(t *testing.T) {
	cases := []struct {
		endpoint string
		want     bool
		why      string
	}{
		{"192.168.1.50", true, "RFC1918"},
		{"10.0.0.4:57221", true, "RFC1918 with port"},
		{"172.16.4.9", true, "RFC1918 172.16/12"},
		{"127.0.0.1", true, "loopback"},
		{"::1", true, "IPv6 loopback"},
		{"[fd00::1]:8096", true, "IPv6 ULA, bracketed with port"},
		{"fe80::1", true, "link-local"},
		{"172.32.0.1", false, "just outside 172.16/12"},
		{"8.8.8.8", false, "public"},
		{"203.0.113.7:44100", false, "public with port"},
		{"", false, "empty address must not be treated as local"},
		{"not-an-ip", false, "unparseable must not be treated as local"},
	}
	for _, c := range cases {
		if got := embySessionIsLocal(c.endpoint); got != c.want {
			t.Errorf("embySessionIsLocal(%q) = %v, want %v (%s)", c.endpoint, got, c.want, c.why)
		}
	}
}

// playingSession builds a session that counts toward throttling.
func playingSession(id, endpoint string) embySession {
	s := embySession{ID: id, RemoteEndPoint: endpoint}
	s.NowPlayingItem = &embyNowPlaying{Name: "Something"}
	return s
}

func TestEmbyActiveSetAppliesExemption(t *testing.T) {
	sessions := []embySession{
		playingSession("lan", "192.168.1.50"),
		playingSession("remote", "8.8.8.8"),
		{ID: "idle", RemoteEndPoint: "8.8.8.9"},
	}

	off := newEmbyWatcher(flavorJellyfin, "http://x", "k", false)
	got := off.activeSet(sessions)
	_, hasLAN := got["lan"]
	_, hasRemote := got["remote"]
	if len(got) != 2 || !hasLAN || !hasRemote {
		t.Errorf("exemption off: got %v, want both playing sessions", got)
	}

	on := newEmbyWatcher(flavorJellyfin, "http://x", "k", true)
	got = on.activeSet(sessions)
	_, hasRemote = got["remote"]
	if len(got) != 1 || !hasRemote {
		t.Errorf("exemption on: got %v, want only the remote session", got)
	}
}

// The server never says "this session stopped" — it just stops
// including it. Everything downstream depends on that being turned
// into a stop event.
func TestEmbyHandleSessionsDiffsAgainstPrevious(t *testing.T) {
	w := newEmbyWatcher(flavorEmby, "http://x", "k", false)
	ctx := context.Background()

	w.handleSessions(ctx, []embySession{playingSession("a", "8.8.8.8")})
	assertEvent(t, w, "a", true)

	// Same list again: nothing changed, so nothing should be emitted.
	w.handleSessions(ctx, []embySession{playingSession("a", "8.8.8.8")})
	assertNoEvent(t, w)

	w.handleSessions(ctx, []embySession{
		playingSession("a", "8.8.8.8"),
		playingSession("b", "8.8.8.8"),
	})
	assertEvent(t, w, "b", true)

	// "a" disappearing from the list is how a stop is reported.
	w.handleSessions(ctx, []embySession{playingSession("b", "8.8.8.8")})
	assertEvent(t, w, "a", false)

	w.handleSessions(ctx, nil)
	assertEvent(t, w, "b", false)
}

// Pausing must not release the throttle. Letting a pause restore full
// rates hands the uplink to the download client for exactly the gap
// before playback resumes — the moment the stream can least afford it.
func TestEmbyPausedSessionKeepsCounting(t *testing.T) {
	w := newEmbyWatcher(flavorEmby, "http://x", "k", false)
	ctx := context.Background()

	w.handleSessions(ctx, []embySession{playingSession("a", "8.8.8.8")})
	assertEvent(t, w, "a", true)

	paused := playingSession("a", "8.8.8.8")
	paused.PlayState.IsPaused = true
	w.handleSessions(ctx, []embySession{paused})
	assertStillActive(t, w)

	// Resuming is likewise not a change in whether it counts.
	w.handleSessions(ctx, []embySession{playingSession("a", "8.8.8.8")})
	assertStillActive(t, w)

	// Only the session actually ending stops it.
	w.handleSessions(ctx, nil)
	assertEvent(t, w, "a", false)
}

// Someone who pauses and never comes back would otherwise throttle
// downloads for as long as their client holds the session open.
func TestEmbyAbandonedPauseStopsCounting(t *testing.T) {
	w := newEmbyWatcher(flavorEmby, "http://x", "k", false)
	w.paused.timeout = 50 * time.Millisecond
	ctx := context.Background()

	paused := playingSession("a", "8.8.8.8")
	paused.PlayState.IsPaused = true

	w.handleSessions(ctx, []embySession{paused})
	assertEvent(t, w, "a", true) // counts at first

	time.Sleep(60 * time.Millisecond)
	w.handleSessions(ctx, []embySession{paused})
	assertEvent(t, w, "a", false) // past the timeout, treated as abandoned

	// Resuming must bring it straight back.
	w.handleSessions(ctx, []embySession{playingSession("a", "8.8.8.8")})
	assertEvent(t, w, "a", true)
}

func assertEvent(t *testing.T, w *EmbyWatcher, key string, active bool) {
	t.Helper()
	select {
	case ev := <-w.Events():
		if ev.Key != key || ev.Active != active {
			t.Fatalf("got event %+v, want {%s %v}", ev, key, active)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for event {%s %v}", key, active)
	}
}

func assertNoEvent(t *testing.T, w *EmbyWatcher) {
	t.Helper()
	select {
	case ev := <-w.Events():
		t.Fatalf("expected no event, got %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestEmbySnapshotFiltersAndResetsBaseline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Sessions" {
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = rw.Write([]byte(`[
			{"Id":"lan","RemoteEndPoint":"192.168.1.20","NowPlayingItem":{"Name":"A"},"PlayState":{"IsPaused":false}},
			{"Id":"remote","RemoteEndPoint":"8.8.8.8","NowPlayingItem":{"Name":"B"},"PlayState":{"IsPaused":false}},
			{"Id":"paused","RemoteEndPoint":"8.8.8.8","NowPlayingItem":{"Name":"C"},"PlayState":{"IsPaused":true}},
			{"Id":"idle","RemoteEndPoint":"8.8.8.8"}
		]`))
	}))
	defer srv.Close()

	w := newEmbyWatcher(flavorJellyfin, srv.URL, "key", true)
	keys, err := w.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// "lan" is exempted and "idle" has nothing loaded; "paused" counts,
	// because a pause does not end a stream.
	got := map[string]bool{}
	for _, k := range keys {
		got[k.Key] = true
	}
	if len(keys) != 2 || !got["remote"] || !got["paused"] {
		t.Fatalf("got %v, want [remote paused]", keys)
	}

	// The baseline must now match, so a push of the same state is
	// silent rather than re-announcing what is already known.
	stillPaused := playingSession("paused", "8.8.8.8")
	stillPaused.PlayState.IsPaused = true
	w.handleSessions(context.Background(), []embySession{
		playingSession("remote", "8.8.8.8"),
		stillPaused,
	})
	assertNoEvent(t, w)
}

// A failed snapshot must not be mistaken for "nothing is playing" —
// the manager leaves state alone on error, and that only holds if the
// watcher reports the failure instead of an empty list.
func TestEmbySnapshotDoesNotClearBaselineOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	w := newEmbyWatcher(flavorEmby, srv.URL, "k", false)
	w.handleSessions(context.Background(), []embySession{playingSession("a", "8.8.8.8")})
	assertEvent(t, w, "a", true)

	if _, err := w.Snapshot(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	w.mu.Lock()
	_, still := w.active["a"]
	w.mu.Unlock()
	if !still {
		t.Error("a failed snapshot must not clear the tracked session set")
	}
}

func TestTestEmbyFlavorStatusHandling(t *testing.T) {
	cases := []struct {
		status  int
		wantErr bool
		wantMsg string
	}{
		{http.StatusOK, false, ""},
		{http.StatusUnauthorized, true, "API key was rejected"},
		{http.StatusForbidden, true, "API key was rejected"},
		{http.StatusNotFound, true, "does not look like"},
		{http.StatusBadGateway, true, "unexpected status"},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/System/Info" {
				t.Errorf("tested against %s, want /System/Info (the public variant would 200 for any key)", r.URL.Path)
			}
			rw.WriteHeader(c.status)
		}))
		err := TestEmby(context.Background(), srv.URL, "key")
		srv.Close()

		if c.wantErr {
			if err == nil {
				t.Errorf("status %d: expected an error", c.status)
			} else if !strings.Contains(err.Error(), c.wantMsg) {
				t.Errorf("status %d: error %q does not mention %q", c.status, err, c.wantMsg)
			}
			continue
		}
		if err != nil {
			t.Errorf("status %d: unexpected error %v", c.status, err)
		}
	}
}

func TestEmbyFlavorsSendTheirOwnAuth(t *testing.T) {
	var gotEmby, gotJellyfin *http.Request
	capture := func(dst **http.Request) http.HandlerFunc {
		return func(rw http.ResponseWriter, r *http.Request) {
			clone := r.Clone(context.Background())
			*dst = clone
			rw.WriteHeader(http.StatusOK)
		}
	}

	srv := httptest.NewServer(capture(&gotEmby))
	if err := TestEmby(context.Background(), srv.URL, "secret"); err != nil {
		t.Fatalf("TestEmby: %v", err)
	}
	srv.Close()

	srv = httptest.NewServer(capture(&gotJellyfin))
	if err := TestJellyfin(context.Background(), srv.URL, "secret"); err != nil {
		t.Fatalf("TestJellyfin: %v", err)
	}
	srv.Close()

	if got := gotEmby.Header.Get("X-Emby-Token"); got != "secret" {
		t.Errorf("Emby X-Emby-Token = %q, want %q", got, "secret")
	}
	auth := gotJellyfin.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "MediaBrowser ") || !strings.Contains(auth, `Token="secret"`) {
		t.Errorf("Jellyfin Authorization = %q, want a MediaBrowser header carrying the token", auth)
	}
}

func TestEmbyWebSocketURL(t *testing.T) {
	emby := newEmbyWatcher(flavorEmby, "http://host:8096", "a b", false)
	got := emby.webSocketURL()
	if !strings.HasPrefix(got, "ws://host:8096/embywebsocket?") {
		t.Errorf("Emby socket URL = %q", got)
	}
	if !strings.Contains(got, "api_key=a+b") {
		t.Errorf("Emby socket URL did not escape the key: %q", got)
	}

	jf := newEmbyWatcher(flavorJellyfin, "https://host/", "k", false)
	got = jf.webSocketURL()
	if !strings.HasPrefix(got, "wss://host/socket?") {
		t.Errorf("Jellyfin socket URL = %q", got)
	}
	if !strings.Contains(got, "deviceId=autothrottle") {
		t.Errorf("Jellyfin socket URL missing device id: %q", got)
	}
}

func TestWebSocketBase(t *testing.T) {
	cases := map[string]string{
		"http://a":  "ws://a",
		"https://a": "wss://a",
		"ws://a":    "ws://a",
	}
	for in, want := range cases {
		if got := webSocketBase(in); got != want {
			t.Errorf("webSocketBase(%q) = %q, want %q", in, got, want)
		}
	}
}

// Guards the shape of the subscribe message: "0,N" as a JSON *string*,
// not a number or an object. A server that does not recognise it
// simply never sends sessions, which would look like "nothing is ever
// playing" rather than like an error.
func TestEmbySubscribeMessageShape(t *testing.T) {
	data := json.RawMessage(`"0,1500"`)
	b, err := json.Marshal(embyMessage{MessageType: "SessionsStart", Data: data})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"MessageType":"SessionsStart","Data":"0,1500"}` {
		t.Errorf("subscribe message = %s", b)
	}

	b, err = json.Marshal(embyMessage{MessageType: "KeepAlive"})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"MessageType":"KeepAlive"}` {
		t.Errorf("keepalive message = %s", b)
	}
}

// A pause stops the position, so "has it moved" alone never noticed the
// one change that mattered: the Dashboard went on showing the stream as
// playing, clock running, until the next reconcile poll.
func TestEmbyPauseIsReportedAsItHappens(t *testing.T) {
	w := newEmbyWatcher(flavorJellyfin, "http://x", "k", false)
	ctx := context.Background()

	s := playingSession("a", "8.8.8.8")
	s.PlayState.PositionTicks = 600_000_000
	w.handleSessions(ctx, []embySession{s})
	assertEvent(t, w, "a", true)

	s.PlayState.IsPaused = true // same position, now paused
	w.handleSessions(ctx, []embySession{s})
	select {
	case ev := <-w.Events():
		if !ev.DetailOnly || !ev.Paused {
			t.Fatalf("pause reported as %+v, want a detail update marked paused", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("pausing produced no update at all")
	}

	s.PlayState.IsPaused = false // and resuming, still at the same spot
	w.handleSessions(ctx, []embySession{s})
	select {
	case ev := <-w.Events():
		if !ev.DetailOnly || ev.Paused {
			t.Fatalf("resume reported as %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("resuming produced no update")
	}
}

// assertStillActive allows a pause or resume to be reported — the
// Dashboard needs to show it — but only as a detail update. Anything
// that would re-resolve throttle state, or end the session, fails.
func assertStillActive(t *testing.T, w *EmbyWatcher) {
	t.Helper()
	select {
	case ev := <-w.Events():
		if !ev.DetailOnly || !ev.Active {
			t.Fatalf("a pause changed throttle state: %+v", ev)
		}
	case <-time.After(50 * time.Millisecond):
	}
}
