package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// rerouteTransport sends requests for plex.tv to a local server instead.
type rerouteTransport struct {
	to   *url.URL
	next http.RoundTripper
}

func (rt rerouteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host == "plex.tv" {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = rt.to.Scheme, rt.to.Host
	}
	return rt.next.RoundTrip(r)
}

// fakePlexPins answers plex.tv's PIN endpoints: a PIN on request, and a
// token once approved is set.
func fakePlexPins(t *testing.T) (approved, down *atomic.Bool) {
	approved, down = new(atomic.Bool), new(atomic.Bool)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case down.Load():
			w.WriteHeader(http.StatusServiceUnavailable)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/pins":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":42,"code":"abcd"}`))
		case r.URL.Path == "/api/v2/pins/42" && approved.Load():
			_, _ = w.Write([]byte(`{"authToken":"plex-token"}`))
		case r.URL.Path == "/api/v2/pins/42":
			_, _ = w.Write([]byte(`{"authToken":null}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	to, _ := url.Parse(srv.URL)
	original := http.DefaultTransport
	http.DefaultTransport = rerouteTransport{to: to, next: original}
	t.Cleanup(func() { http.DefaultTransport = original })
	return approved, down
}

func panelState(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	expectStatus(t, rec, http.StatusOK)
	expectBody(t, rec, `data-state="`+want+`"`)
}

// The panel walks start, wait, done, and hands the token to the form
// without saving anything.
func TestPlexSignInHandsOverTheToken(t *testing.T) {
	approved, _ := fakePlexPins(t)
	app := newApp(t)

	rec := app.post("/configuration/plex-signin", nil)
	panelState(t, rec, "waiting")
	expectBody(t, rec, "code=abcd")

	panelState(t, app.get("/configuration/plex-signin/poll"), "waiting")
	approved.Store(true)
	rec = app.get("/configuration/plex-signin/poll")
	panelState(t, rec, "done")
	expectBody(t, rec, "plex-token")

	// The attempt is spent: polling again does not hand the token out twice.
	panelState(t, app.get("/configuration/plex-signin/poll"), "error")
	if servers, _ := app.db.ListMediaServers(); len(servers) != 0 {
		t.Errorf("sign-in saved a server: %+v", servers)
	}
}

func TestPlexSignInFailures(t *testing.T) {
	_, down := fakePlexPins(t)
	app := newApp(t)

	// Polling with nothing started, and cancelling, end in the error state.
	panelState(t, app.get("/configuration/plex-signin/poll"), "error")
	panelState(t, app.post("/configuration/plex-signin", nil), "waiting")
	panelState(t, app.post("/configuration/plex-signin/cancel", nil), "error")
	panelState(t, app.get("/configuration/plex-signin/poll"), "error")

	// An attempt older than Plex keeps its PINs is not polled.
	panelState(t, app.post("/configuration/plex-signin", nil), "waiting")
	app.srv.plex.mu.Lock()
	app.srv.plex.started = time.Now().Add(-plexPinTTL - time.Minute)
	app.srv.plex.mu.Unlock()
	panelState(t, app.get("/configuration/plex-signin/poll"), "error")

	// plex.tv failing, at the start and mid-attempt.
	panelState(t, app.post("/configuration/plex-signin", nil), "waiting")
	down.Store(true)
	panelState(t, app.get("/configuration/plex-signin/poll"), "error")
	rec := app.post("/configuration/plex-signin", nil)
	panelState(t, rec, "error")
	if strings.Contains(rec.Body.String(), "data-authurl") {
		t.Error("a failed start still links to Plex")
	}
}
