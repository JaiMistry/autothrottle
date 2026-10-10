package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakePlex is a Plex server: its root, /status/sessions, and the
// notification socket, whose messages the test pushes.
type fakePlex struct {
	*httptest.Server
	mu           sync.Mutex
	rootStatus   int
	sessions     []map[string]any
	sessionsCode int
	lookups      int
	wsQuery      chan string
	push         chan string
}

func newFakePlex(t *testing.T) *fakePlex {
	t.Helper()
	f := &fakePlex{rootStatus: http.StatusOK, sessionsCode: http.StatusOK, wsQuery: make(chan string, 1), push: make(chan string, 8)}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/":
			w.WriteHeader(f.rootStatus)
		case "/status/sessions":
			f.lookups++
			if f.sessionsCode != http.StatusOK {
				w.WriteHeader(f.sessionsCode)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{"Metadata": f.sessions}})
		case "/:/websockets/notifications":
			f.mu.Unlock()
			defer f.mu.Lock()
			f.socket(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakePlex) socket(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = c.CloseNow() }()
	select {
	case f.wsQuery <- r.URL.RawQuery:
	default:
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-f.push:
			if err := c.Write(r.Context(), websocket.MessageText, []byte(msg)); err != nil {
				return
			}
		}
	}
}

func (f *fakePlex) set(sessions ...map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = sessions
}

func (f *fakePlex) lookupCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lookups
}

func playing(key, state string, offsetMS int) string {
	return fmt.Sprintf(`{"NotificationContainer":{"type":"playing","PlaySessionStateNotification":[{"sessionKey":%q,"state":%q,"viewOffset":%d}]}}`, key, state, offsetMS)
}

func episode(key, state string, local bool) map[string]any {
	return map[string]any{
		"sessionKey": key, "type": "episode", "title": "Rogue", "grandparentTitle": "Andor",
		"parentIndex": 1, "index": 4, "viewOffset": 761000, "duration": 2840000,
		"Player": map[string]any{"state": state, "local": local, "title": "Living Room TV", "device": "Chromecast"},
	}
}

// fakePlexTV stands in for plex.tv: the account check and the sign-in PINs.
func fakePlexTV(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	account, pins, auth := plexAccountURL, plexPinsURL, plexAuthURL
	plexAccountURL, plexPinsURL, plexAuthURL = srv.URL+"/api/v2/user", srv.URL+"/api/v2/pins", srv.URL+"/auth"
	t.Cleanup(func() { plexAccountURL, plexPinsURL, plexAuthURL = account, pins, auth })
}

func nextEvent(t *testing.T, w *PlexWatcher) SessionEvent {
	t.Helper()
	select {
	case e := <-w.Events():
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
		return SessionEvent{}
	}
}

func noEvent(t *testing.T, w *PlexWatcher) {
	t.Helper()
	select {
	case e := <-w.Events():
		t.Fatalf("unexpected event %+v", e)
	case <-time.After(150 * time.Millisecond):
	}
}

// The server answering proves only that it is up: Plex accepts any token
// on the LAN, so the token is checked with plex.tv.
func TestPlexConnectionTest(t *testing.T) {
	cases := []struct {
		name         string
		root, plexTV int
		closed       bool
		want         string
	}{
		{name: "both accept", root: 200, plexTV: 200},
		{name: "server refuses the token", root: 401, plexTV: 200, want: "token was rejected"},
		{name: "server errors", root: 500, plexTV: 200, want: "unexpected status 500"},
		{name: "plex.tv refuses the token", root: 200, plexTV: 401, want: "plex.tv rejected this token"},
		{name: "plex.tv forbids it", root: 200, plexTV: 403, want: "plex.tv rejected this token"},
		{name: "plex.tv errors", root: 200, plexTV: 502, want: "plex.tv answered 502"},
		{name: "plex.tv unreachable", root: 200, closed: true, want: "could not be checked with plex.tv"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := newFakePlex(t)
			server.rootStatus = c.root
			var token string
			fakePlexTV(t, func(w http.ResponseWriter, r *http.Request) {
				token = r.Header.Get("X-Plex-Token")
				w.WriteHeader(c.plexTV)
			})
			if c.closed {
				plexAccountURL = "http://127.0.0.1:1/api/v2/user"
			}
			err := TestMediaServer(context.Background(), "plex", server.URL+"/", "tok")
			if c.want == "" {
				if err != nil || token != "tok" {
					t.Fatalf("err %v, plex.tv saw token %q", err, token)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want %q", err, c.want)
			}
		})
	}

	if err := TestPlex(context.Background(), "http://127.0.0.1:1", "tok"); err == nil || !strings.Contains(err.Error(), "connecting to") {
		t.Errorf("unreachable server: %v", err)
	}
	if err := TestPlex(context.Background(), "http://bad host", "tok"); err == nil {
		t.Error("an unparseable address passed")
	}
}

func TestMediaServerTypes(t *testing.T) {
	if !SupportedMediaServerType("jellyfin") || SupportedMediaServerType("kodi") {
		t.Error("SupportedMediaServerType")
	}
	if err := TestMediaServer(context.Background(), "kodi", "http://x", "k"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("unknown type: %v", err)
	}
	for _, kind := range []string{"emby", "jellyfin"} {
		if err := TestMediaServer(context.Background(), kind, "http://127.0.0.1:1", "k"); err == nil {
			t.Errorf("%s: an unreachable server passed", kind)
		}
	}
}

func TestPlexSignIn(t *testing.T) {
	var mu sync.Mutex
	pinStatus, pinBody := http.StatusOK, `{"id":7,"authToken":null}`
	createStatus, createBody := http.StatusCreated, `{"id":7,"code":"abcd"}`
	var clientIDs []string
	fakePlexTV(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		clientIDs = append(clientIDs, r.Header.Get("X-Plex-Client-Identifier"))
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/pins":
			if err := r.ParseForm(); err != nil || r.FormValue("strong") != "true" {
				http.Error(w, "want a strong pin", http.StatusBadRequest)
				return
			}
			w.WriteHeader(createStatus)
			_, _ = w.Write([]byte(createBody))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/pins/7":
			w.WriteHeader(pinStatus)
			_, _ = w.Write([]byte(pinBody))
		default:
			http.NotFound(w, r)
		}
	})
	setPin := func(status int, body string) { mu.Lock(); pinStatus, pinBody = status, body; mu.Unlock() }
	ctx := context.Background()

	pin, err := RequestPlexPin(ctx, "client-1")
	if err != nil || pin.ID != 7 || pin.Code != "abcd" || pin.ClientID != "client-1" {
		t.Fatalf("pin %+v, %v", pin, err)
	}
	for _, want := range []string{"/auth#?", "clientID=client-1", "code=abcd", "AutoThrottle"} {
		if !strings.Contains(pin.AuthURL, want) {
			t.Errorf("auth URL %q lacks %q", pin.AuthURL, want)
		}
	}

	if token, err := PollPlexPin(ctx, "client-1", 7); token != "" || err != nil {
		t.Errorf("before approval: %q, %v", token, err)
	}
	setPin(http.StatusOK, `{"id":7,"authToken":"granted"}`)
	if token, err := PollPlexPin(ctx, "client-1", 7); token != "granted" || err != nil {
		t.Errorf("after approval: %q, %v", token, err)
	}
	mu.Lock()
	for _, id := range clientIDs {
		if id != "client-1" {
			t.Errorf("plex.tv saw client %q; it ties the PIN to one", id)
		}
	}
	mu.Unlock()

	for status, want := range map[int]string{http.StatusNotFound: "expired", http.StatusInternalServerError: "answered 500"} {
		setPin(status, "")
		if _, err := PollPlexPin(ctx, "client-1", 7); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("poll %d: %v", status, err)
		}
	}
	setPin(http.StatusOK, "not json")
	if _, err := PollPlexPin(ctx, "client-1", 7); err == nil {
		t.Error("an unreadable poll reply passed")
	}

	mu.Lock()
	createStatus = http.StatusTooManyRequests
	mu.Unlock()
	if _, err := RequestPlexPin(ctx, "client-1"); err == nil || !strings.Contains(err.Error(), "answered 429") {
		t.Errorf("refused pin: %v", err)
	}
	mu.Lock()
	createStatus, createBody = http.StatusCreated, "not json"
	mu.Unlock()
	if _, err := RequestPlexPin(ctx, "client-1"); err == nil {
		t.Error("an unreadable pin passed")
	}

	plexPinsURL = "http://127.0.0.1:1/api/v2/pins"
	if _, err := RequestPlexPin(ctx, "client-1"); err == nil || !strings.Contains(err.Error(), "contacting plex.tv") {
		t.Errorf("unreachable: %v", err)
	}
	if _, err := PollPlexPin(ctx, "client-1", 7); err == nil || !strings.Contains(err.Error(), "contacting plex.tv") {
		t.Errorf("unreachable poll: %v", err)
	}
	plexPinsURL = "http://bad host"
	if _, err := RequestPlexPin(ctx, "c"); err == nil {
		t.Error("an unparseable address passed")
	}
	if _, err := PollPlexPin(ctx, "c", 7); err == nil {
		t.Error("an unparseable poll address passed")
	}
}

// A session's first notification starts it, later ones only move its
// position, and a stop ends it. What is playing comes from a follow-up
// lookup, since notifications carry only a key.
func TestPlexWatcherNotifications(t *testing.T) {
	server := newFakePlex(t)
	server.set(episode("1", "playing", false))
	w := NewPlexWatcher(server.URL+"/", "tok", false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	if q := <-server.wsQuery; !strings.Contains(q, "X-Plex-Token=tok") {
		t.Errorf("socket query %q", q)
	}
	server.push <- `not json`
	server.push <- `{"NotificationContainer":{"type":"timeline"}}`
	server.push <- playing("", "playing", 0)
	server.push <- playing("1", "playing", 60000)

	started := nextEvent(t, w)
	if !started.Active || started.DetailOnly || started.Key != "1" || started.Position != time.Minute {
		t.Fatalf("start %+v", started)
	}
	if !w.Connection().Connected {
		t.Error("not connected with the socket open")
	}
	detail := nextEvent(t, w)
	if !detail.DetailOnly || detail.Title != "Andor S1E4" || detail.Device != "Living Room TV" || detail.Duration != 2840*time.Second {
		t.Fatalf("detail %+v", detail)
	}

	server.push <- playing("1", "paused", 62000)
	if moved := nextEvent(t, w); !moved.DetailOnly || !moved.Paused || moved.Position != 62*time.Second {
		t.Fatalf("position update %+v", moved)
	}
	server.push <- playing("1", "stopped", 62000)
	if ended := nextEvent(t, w); ended.Active || ended.Key != "1" {
		t.Fatalf("stop %+v", ended)
	}
}

// With the exemption on, a local session never starts, and the lookup
// that decides it is made once per session. A failed lookup counts as
// not local: being unsure must not stop throttling.
func TestPlexWatcherSkipsLocalPlayback(t *testing.T) {
	server := newFakePlex(t)
	server.set(episode("local", "playing", true))
	w := NewPlexWatcher(server.URL, "tok", true)
	ctx := context.Background()

	w.handleMessage(ctx, []byte(playing("local", "playing", 1000)))
	w.handleMessage(ctx, []byte(playing("local", "playing", 3000)))
	noEvent(t, w)
	if n := server.lookupCount(); n != 1 {
		t.Errorf("%d lookups for one session", n)
	}

	w.handleMessage(ctx, []byte(playing("gone", "playing", 1000)))
	if e := nextEvent(t, w); !e.Active || e.Key != "gone" {
		t.Errorf("unknown locality: %+v", e)
	}
	server.mu.Lock()
	server.sessionsCode = http.StatusBadGateway
	server.mu.Unlock()
	w.handleMessage(ctx, []byte(playing("broken", "playing", 1000)))
	if e := nextEvent(t, w); !e.Active || e.Key != "broken" {
		t.Errorf("failed lookup: %+v", e)
	}
}

// A pause left for too long ends the session, without a stop.
func TestPlexWatcherReleasesAbandonedPause(t *testing.T) {
	w := NewPlexWatcher("http://127.0.0.1:1", "tok", false)
	w.paused = newPausedTracker(10 * time.Millisecond)
	ctx := context.Background()

	w.handleMessage(ctx, []byte(playing("1", "paused", 5000)))
	if e := nextEvent(t, w); !e.Active {
		t.Fatalf("pause %+v", e)
	}
	time.Sleep(20 * time.Millisecond)
	w.handleMessage(ctx, []byte(playing("1", "paused", 5000)))
	if e := nextEvent(t, w); e.Active || e.Key != "1" {
		t.Fatalf("abandoned pause %+v", e)
	}
}

func TestPlexSnapshot(t *testing.T) {
	server := newFakePlex(t)
	movie := map[string]any{"sessionKey": "m", "type": "movie", "title": "Heat", "viewOffset": 1000, "duration": 2000,
		"Player": map[string]any{"state": "buffering", "device": "Chromecast"}}
	showOnly := episode("s", "paused", false)
	delete(showOnly, "index")
	server.set(
		episode("1", "playing", false),
		episode("local", "playing", true),
		episode("stopped", "stopped", false),
		map[string]any{"sessionKey": "", "Player": map[string]any{"state": "playing"}},
		movie, showOnly,
	)
	w := NewPlexWatcher(server.URL, "tok", true)
	w.lastPos["ended"] = time.Second
	w.localCache["ended"] = false

	got, err := w.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]Session{}
	for _, s := range got {
		byKey[s.Key] = s
	}
	if len(got) != 3 || byKey["1"].Title != "Andor S1E4" || byKey["m"].Title != "Heat" || byKey["m"].Device != "Chromecast" ||
		byKey["s"].Title != "Andor" || !byKey["s"].Paused {
		t.Fatalf("snapshot %+v", got)
	}
	if _, ok := w.lastPos["ended"]; ok {
		t.Error("an ended session's position was kept")
	}
	if local, ok := w.cachedLocal("local"); !ok || !local {
		t.Error("the snapshot did not record locality")
	}

	server.mu.Lock()
	server.sessionsCode = http.StatusUnauthorized
	server.mu.Unlock()
	if _, err := w.Snapshot(context.Background()); err == nil {
		t.Error("a refused snapshot passed")
	}
}
