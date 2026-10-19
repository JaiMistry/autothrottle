package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/JaiMistry/autothrottle/internal/clients"
)

// listener records everything sent to it: someone else's machine.
type listener struct {
	*httptest.Server
	mu   sync.Mutex
	seen []string
}

func newListener(t *testing.T) *listener {
	l := &listener{}
	l.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		l.mu.Lock()
		l.seen = append(l.seen, fmt.Sprint(r.Header, r.URL.RawQuery, string(body)))
		l.mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(l.Close)
	return l
}

func (l *listener) heard(secret string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.seen {
		if strings.Contains(s, secret) {
			return true
		}
	}
	return false
}

// Pointing a saved server elsewhere and pressing Test or Save must not
// send the saved token there.
func TestSavedTokenNeverGoesToANewAddress(t *testing.T) {
	app := newApp(t)
	jf := fakeJellyfin(t)
	app.post("/configuration/media-servers", mediaServerForm("JF", "jellyfin", jf.URL, "good-key"))
	servers, _ := app.db.ListMediaServers()
	id := fmt.Sprint(servers[0].ID)
	thief := newListener(t)

	form := mediaServerForm("JF", "jellyfin", thief.URL, "")
	form.Set("id", id)
	rec := app.post("/configuration/media-servers/test", form)
	expectBody(t, rec, "Nothing to test")
	if thief.heard("good-key") {
		t.Fatal("Test sent the saved token to a new address")
	}

	rec = app.post("/configuration/media-servers", form)
	expectStatus(t, rec, http.StatusUnprocessableEntity)
	expectBody(t, rec, "Enter the token again for the new address")
	if after, _ := app.db.GetMediaServer(servers[0].ID); after.URL != jf.URL {
		t.Errorf("the address changed to %s with the saved token", after.URL)
	}

	// Another path on the same server is still that server.
	same := mediaServerForm("JF", "jellyfin", jf.URL+"/", "")
	same.Set("id", id)
	expectBody(t, app.post("/configuration/media-servers/test", same), `data-state="connected"`)
	expectRedirect(t, app.post("/configuration/media-servers", same), "/configuration?tab=connections")

	// A token typed for the new address is the user's own to send.
	moved := mediaServerForm("JF", "jellyfin", thief.URL, "new-key")
	moved.Set("id", id)
	expectRedirect(t, app.post("/configuration/media-servers", moved), "/configuration?tab=connections")
}

// The same for a downloader's password; an edit that leaves it blank
// keeps it.
func TestSavedPasswordStaysWithItsAddress(t *testing.T) {
	app := newApp(t)
	form := url.Values{"name": {"qBit"}, "type": {"qbittorrent"}, "url": {app.qbit.URL}, "enabled": {"on"},
		"username": {"admin"}, "password": {"hunter2"}}
	expectRedirect(t, app.post("/configuration/downloaders", form), "/configuration?tab=connections")
	dls, _ := app.db.ListDownloaders()
	id := fmt.Sprint(dls[0].ID)
	before := dls[0].CredentialEncrypted

	// As the edit form sends it: username filled in, password blank.
	edit := url.Values{"id": {id}, "name": {"qBit renamed"}, "type": {"qbittorrent"}, "url": {app.qbit.URL}, "enabled": {"on"}, "username": {"admin"}}
	expectRedirect(t, app.post("/configuration/downloaders", edit), "/configuration?tab=connections")
	if after, _ := app.db.GetDownloader(dls[0].ID); after.Name != "qBit renamed" || after.CredentialEncrypted != before {
		t.Errorf("editing without the password: %+v", after)
	}

	thief := newListener(t)
	moved := url.Values{"id": {id}, "name": {"qBit"}, "type": {"qbittorrent"}, "url": {thief.URL}, "enabled": {"on"}, "username": {"admin"}}
	rec := app.post("/configuration/downloaders", moved)
	expectStatus(t, rec, http.StatusUnprocessableEntity)
	expectBody(t, rec, "Enter the password again")
	app.post("/configuration/downloaders/test", moved)
	app.sched.ResolveAsync()
	if thief.heard("hunter2") {
		t.Fatal("the saved password reached a new address")
	}

	// A new username with the old password is refused too.
	renamed := url.Values{"id": {id}, "name": {"qBit"}, "type": {"qbittorrent"}, "url": {app.qbit.URL}, "enabled": {"on"}, "username": {"someone-else"}}
	expectStatus(t, app.post("/configuration/downloaders", renamed), http.StatusUnprocessableEntity)
	if after, _ := app.db.GetDownloader(dls[0].ID); after.URL != app.qbit.URL || after.CredentialEncrypted != before {
		t.Errorf("a refused edit changed the downloader: %+v", after)
	}
}

func TestSameOrigin(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"http://192.168.1.12:32400", "http://192.168.1.12:32400/", true},
		{"http://Plex.lan", "http://plex.lan:80/web", true},
		{"https://plex.example.com", "https://plex.example.com:443", true},
		{"http://192.168.1.12:32400", "http://192.168.1.13:32400", false},
		{"http://192.168.1.12:32400", "http://192.168.1.12:32401", false},
		{"http://plex.lan", "https://plex.lan", false},
		{"http://plex.lan", "not a url", false},
	} {
		if got := sameOrigin(c.a, c.b); got != c.want {
			t.Errorf("sameOrigin(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	app := newApp(t)
	for _, path := range []string{"/", "/login", "/static/css/app.css", "/no-such-page"} {
		rec := app.get(path)
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Referrer-Policy") != "same-origin" {
			t.Errorf("%s: %v", path, rec.Header())
		}
	}
}

// A test that passed with a caveat reads Connected, with the caveat
// under it, and allows saving.
func TestACaveatIsAPassWithANote(t *testing.T) {
	app := newApp(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/configuration/media-servers/test", nil)
	app.srv.probe(rec, req, probeServer, "http://plex.lan:32400", "Successfully connected.", func(context.Context) error {
		return &clients.Caveat{Note: "plex.tv couldn't be reached"}
	})
	expectBody(t, rec, `data-state="connected"`)
	expectBody(t, rec, "Successfully connected.")
	expectBody(t, rec, "plex.tv couldn&#39;t be reached")
}
