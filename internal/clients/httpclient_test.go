package clients

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
)

// elsewhere stands in for whatever a redirect points at, counting what
// reaches it.
func elsewhere(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// redirector sends every request on to target, keeping the path.
func redirector(t *testing.T, status int, target string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target+r.URL.Path, status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A 307 repeats the login form, password and all, wherever it points.
func TestQBittorrentLoginIsNotRedirectedElsewhere(t *testing.T) {
	other, hits := elsewhere(t)
	srv := redirector(t, http.StatusTemporaryRedirect, other.URL)

	err := TestQBittorrent(context.Background(), srv.URL, "admin", "secret")
	if err == nil || !strings.Contains(err.Error(), "redirected to another address") {
		t.Fatalf("want the redirect refused, got %v", err)
	}
	if hits.Load() != 0 {
		t.Errorf("the login reached the other server %d time(s)", hits.Load())
	}
}

// Go would forward X-Emby-Token and X-Plex-Token to another host itself.
func TestTokenHeaderIsNotRedirectedElsewhere(t *testing.T) {
	other, hits := elsewhere(t)
	srv := redirector(t, http.StatusFound, other.URL)

	var out any
	err := getJSON(context.Background(), srv.URL+"/Sessions", func(r *http.Request) {
		r.Header.Set("X-Emby-Token", "k")
	}, &out)
	if err == nil || !strings.Contains(err.Error(), "redirected to another address") {
		t.Fatalf("want the redirect refused, got %v", err)
	}
	if hits.Load() != 0 {
		t.Errorf("the token reached the other server %d time(s)", hits.Load())
	}
}

// The WebSocket library follows redirects with the client it is given.
func TestWebSocketIsNotRedirectedElsewhere(t *testing.T) {
	other, hits := elsewhere(t)
	srv := redirector(t, http.StatusFound, other.URL)

	u, _ := url.Parse(srv.URL)
	u.Scheme = "ws"
	_, _, err := websocket.Dial(context.Background(), u.String()+"/socket", &websocket.DialOptions{HTTPClient: newHTTPClient(0)})
	if err == nil || !strings.Contains(err.Error(), "redirected to another address") {
		t.Fatalf("want the redirect refused, got %v", err)
	}
	if hits.Load() != 0 {
		t.Errorf("the handshake reached the other server %d time(s)", hits.Load())
	}
}

// A server moving its own paths around still works.
func TestRedirectOnTheSameServerIsFollowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusFound)
			return
		}
		if r.Header.Get("X-Emby-Token") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var out struct{ OK bool }
	err := getJSON(context.Background(), srv.URL+"/old", func(r *http.Request) {
		r.Header.Set("X-Emby-Token", "k")
	}, &out)
	if err != nil || !out.OK {
		t.Fatalf("want the redirect followed with the token, got %v, %+v", err, out)
	}
}

func TestFollowSameServer(t *testing.T) {
	cases := []struct {
		from, to string
		ok       bool
	}{
		{"http://plex.lan:32400/", "http://plex.lan:32400/web", true},
		{"http://plex.lan:32400/", "https://plex.lan:32400/", true},
		{"http://PLEX.lan:32400/", "http://plex.lan:32400/", true},
		{"http://plex.lan:32400/", "http://plex.lan:32401/", false},
		{"http://plex.lan:32400/", "http://auth.example/", false},
		{"https://plex.example/", "http://plex.example/", false},
		// A WebSocket's first request is ws or wss, the redirect http or https.
		{"ws://emby.lan:8096/", "http://emby.lan:8096/socket", true},
		{"wss://emby.example/", "https://emby.example/socket", true},
		{"wss://emby.example/", "http://emby.example/socket", false},
	}
	for _, c := range cases {
		from, _ := http.NewRequest(http.MethodGet, c.from, nil)
		to, _ := http.NewRequest(http.MethodGet, c.to, nil)
		err := followSameServer(to, []*http.Request{from})
		if (err == nil) != c.ok {
			t.Errorf("%s -> %s: got %v, want followed = %v", c.from, c.to, err, c.ok)
		}
	}

	from, _ := http.NewRequest(http.MethodGet, "http://plex.lan/", nil)
	via := make([]*http.Request, 10)
	for i := range via {
		via[i] = from
	}
	if err := followSameServer(from, via); err == nil {
		t.Error("an eleventh redirect was followed")
	}
}
