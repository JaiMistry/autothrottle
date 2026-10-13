package web

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/live"
)

type stream struct {
	t     *testing.T
	resp  *http.Response
	lines chan string
}

// openStream connects with the session cookie, over a real server whose
// write timeout is short: the stream has to outlive it.
func (a *testApp) openStream() *stream {
	a.t.Helper()
	srv := httptest.NewUnstartedServer(a.handler)
	srv.Config.WriteTimeout = 500 * time.Millisecond
	srv.Start()
	a.t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	a.t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/events", nil)
	for _, c := range a.cookies {
		req.AddCookie(c)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	a.t.Cleanup(func() { _ = resp.Body.Close() })
	st := &stream{t: a.t, resp: resp, lines: make(chan string, 64)}
	go func() {
		defer close(st.lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			st.lines <- sc.Text()
		}
	}()
	return st
}

// expect reads until it sees want, failing on timeout or a closed stream.
func (st *stream) expect(want string) {
	st.t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case line, ok := <-st.lines:
			if !ok {
				st.t.Fatalf("the stream closed before %q", want)
			}
			if line == want {
				return
			}
		case <-timeout:
			st.t.Fatalf("no %q on the stream", want)
		}
	}
}

func TestEventStream(t *testing.T) {
	app := newApp(t)
	st := app.openStream()
	if st.resp.StatusCode != http.StatusOK || st.resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("%d %q", st.resp.StatusCode, st.resp.Header.Get("Content-Type"))
	}
	if st.resp.Header.Get("Cache-Control") != "no-cache" || st.resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Errorf("headers %v", st.resp.Header)
	}
	st.expect("retry: 3000")

	// Past the server's write timeout, still delivering.
	time.Sleep(700 * time.Millisecond)
	app.srv.Live().Notify(live.Logs)
	st.expect("data: logs")
	app.srv.Live().Notify(live.Dashboard)
	st.expect("data: dashboard")
}

func TestEventStreamCarriesRealChanges(t *testing.T) {
	app := newApp(t)
	st := app.openStream()
	st.expect("retry: 3000")

	expectStatus(t, app.post("/pause-toggle", nil), http.StatusSeeOther)
	st.expect("data: dashboard")

	if err := app.db.InsertEvent(db.Event{Level: "info", Message: "hello"}); err != nil {
		t.Fatal(err)
	}
	st.expect("data: logs")
	expectStatus(t, app.post("/logs/clear", nil), http.StatusOK)
	st.expect("data: logs")
}

func TestEventStreamNeedsSignIn(t *testing.T) {
	app := newAppWith(t, map[string]string{"AUTH_USERNAME": testUser, "AUTH_PASSWORD": testPass})
	rec := app.get("/events")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("signed out: %d → %q", rec.Code, rec.Header().Get("Location"))
	}
	if n := app.srv.Live().Subscribers(); n != 0 {
		t.Errorf("%d subscribers after a refused stream", n)
	}
}

// htmx follows a plain redirect and swaps the page it lands on in, which
// put the sign-in form inside the app once a session expired.
func TestExpiredSessionLoadsTheSignInPage(t *testing.T) {
	app := newAppWith(t, map[string]string{"AUTH_USERNAME": testUser, "AUTH_PASSWORD": testPass})
	app.htmx = true
	rec := app.get("/dashboard/live")
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("HX-Redirect") != "/login" || rec.Header().Get("Location") != "" {
		t.Errorf("signed out, from htmx: %d, HX-Redirect %q, Location %q",
			rec.Code, rec.Header().Get("HX-Redirect"), rec.Header().Get("Location"))
	}
}

// Open streams must not hold shutdown up for its whole timeout.
func TestEventStreamEndsOnShutdown(t *testing.T) {
	app := newApp(t)
	st := app.openStream()
	st.expect("retry: 3000")
	app.srv.Live().Close()
	done := make(chan struct{})
	go func() {
		for range st.lines {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the stream outlived the hub")
	}
}

func TestEventStreamClosedByClient(t *testing.T) {
	app := newApp(t)
	st := app.openStream()
	st.expect("retry: 3000")
	if app.srv.Live().Subscribers() != 1 {
		t.Fatalf("%d subscribers", app.srv.Live().Subscribers())
	}
	_ = st.resp.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for app.srv.Live().Subscribers() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("a closed tab kept its subscription")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLiveBlocksAreMarked(t *testing.T) {
	app := newApp(t)
	app.mediaServer("JF", "jellyfin", "http://jf.lan")
	app.downloader("qBit")
	for path, want := range map[string]string{"/": `data-live="dashboard"`, "/logs": `data-live="logs"`} {
		body := app.get(path).Body.String()
		if !strings.Contains(body, want) || !strings.Contains(body, `hx-trigger="live, every 5s [atLivePoll(this)]"`) {
			t.Errorf("%s is not marked live", path)
		}
	}
}
