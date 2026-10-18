package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/db"
)

func (a *testApp) events(n int, level string) {
	a.t.Helper()
	for i := 0; i < n; i++ {
		if err := a.db.InsertEvent(db.Event{Level: level, Message: fmt.Sprintf("%s event %d", level, i)}); err != nil {
			a.t.Fatal(err)
		}
	}
}

var logRow = regexp.MustCompile(`class="at-logrow"`)

func countRows(body string) int { return len(logRow.FindAllString(body, -1)) }

func TestLogsPagingAndFilters(t *testing.T) {
	app := newApp(t)
	app.events(25, "info")
	app.events(3, "warn")
	app.events(2, "error")

	if n := countRows(app.get("/logs?per=10").Body.String()); n != 10 {
		t.Errorf("page one shows %d rows, want 10", n)
	}
	page3 := app.get("/logs?per=10&page=3").Body.String()
	if n := countRows(page3); n != 10 {
		t.Errorf("page three shows %d rows, want 10", n)
	}
	if !strings.Contains(page3, "21–30 of 30") {
		t.Error("page three does not say 21–30 of 30")
	}

	warn := app.get("/logs?level=warn").Body.String()
	if n := countRows(warn); n != 3 {
		t.Errorf("warn filter shows %d rows, want 3", n)
	}
	if n := countRows(app.get("/logs?q=" + url.QueryEscape("error event 1")).Body.String()); n != 1 {
		t.Errorf("search shows %d rows, want 1", n)
	}
	expectBody(t, app.get("/logs?q=nothing-matches-this"), "Nothing matches")
}

// Clear is a real delete, and the page goes on refreshing afterwards.
func TestClearDeletesTheLogAndKeepsRefreshing(t *testing.T) {
	app := newApp(t)
	app.events(5, "info")

	rec := app.post("/logs/clear", nil)
	expectStatus(t, rec, http.StatusOK)
	if events, _ := app.db.ListEvents(100); len(events) != 0 {
		t.Fatalf("%d events survived Clear", len(events))
	}
	expectBody(t, rec, `data-live="logs"`)
	expectBody(t, rec, "No events yet")

	app.events(1, "info")
	if n := countRows(app.get("/logs/panel?per=20").Body.String()); n != 1 {
		t.Errorf("after an event arrives the panel shows %d rows, want 1", n)
	}
}

// waitFor polls until cond holds; the scheduler resolves asynchronously.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func rowStates(body, class string) []string {
	re := regexp.MustCompile(class + `" data-state="([a-z]+)"`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestDashboardThrottleStates(t *testing.T) {
	app := newApp(t)
	idle := app.mediaServer("Idle Server", "jellyfin", "http://idle.lan")
	busy := app.mediaServer("Busy Server", "jellyfin", "http://busy.lan")
	app.watchers.running[idle], app.watchers.running[busy] = true, true
	dl := app.downloader("qBit")
	app.setMapping(busy, dl, true)
	waitFor(t, "the config change to resolve", func() bool { return app.sched.State(dl).Known })

	live := app.get("/dashboard/live").Body.String()
	if !strings.Contains(live, "Nothing is throttled") {
		t.Fatalf("idle dashboard: %.400s", live)
	}

	app.sched.HandleSessionEvent(busy, clients.Session{Key: "s1", Title: "Andor S1E4", Device: "Living Room TV",
		Position: time.Minute, PositionAt: time.Now(), Duration: 40 * time.Minute}, true)
	waitFor(t, "throttling to apply", func() bool { return app.sched.State(dl).Throttled })

	live = app.get("/dashboard/live").Body.String()
	if !strings.Contains(live, "qBit throttled") {
		t.Errorf("the throttled downloader is not named: %.500s", live)
	}
	if !strings.Contains(live, "because Busy Server is playing") {
		t.Errorf("the reason does not name the playing server: %.500s", live)
	}
	if got := rowStates(live, "at-dlrow-wrap"); len(got) != 1 || got[0] != "throttled" {
		t.Errorf("downloader rows = %v", got)
	}
	// The playing server sorts to the top.
	if got := rowStates(live, "at-serverrow"); len(got) != 2 || got[0] != "playing" {
		t.Errorf("server rows = %v, want the playing one first", got)
	}
	if !strings.Contains(live, "Andor S1E4") {
		t.Error("the playing title is not shown")
	}

	app.post("/pause-toggle", nil)
	waitFor(t, "the pause to apply", func() bool { return !app.sched.State(dl).Throttled })
	if !strings.Contains(app.get("/dashboard/live").Body.String(), "Default downloader speeds enforced") {
		t.Error("the paused dashboard does not say throttling is off")
	}
}

func TestUpdateCheck(t *testing.T) {
	cases := []struct {
		name, tag string
		status    int
		running   string
		want      string
	}{
		{"same release", "v1.0.0", 200, "1.0.0", "Up to date"},
		{"newer published", "v1.1.0", 200, "1.0.0", "v1.1.0 available"},
		{"ahead of the latest", "v1.0.0", 200, "1.0.1", "Up to date"},
		{"rc moves to release", "v1.0.0", 200, "1.0.0-rc.2", "v1.0.0 available"},
		{"private or missing repo", "", 404, "1.0.0", "Could not reach GitHub"},
		{"dev build", "v1.0.0", 200, "", "development build"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				if c.status == 200 {
					_, _ = fmt.Fprintf(w, `{"tag_name":%q}`, c.tag)
				}
			}))
			defer gh.Close()
			old := releasesAPI
			releasesAPI = gh.URL
			defer func() { releasesAPI = old }()

			view := checkLatestRelease(t.Context(), c.running)
			text := view.Label + " " + view.NotePre + view.NoteStrong + view.NotePost
			if !strings.Contains(text, c.want) {
				t.Errorf("got %q, want it to mention %q", text, c.want)
			}
		})
	}
}

func TestHeroStatements(t *testing.T) {
	cases := []struct {
		name string
		in   heroInput
		want heroState
	}{
		{"nothing configured past a downloader", heroInput{downloaders: 1}, heroState{Statement: "Not ready to throttle", Tone: "dim", Missing: "a media server"}},
		{"a server but no downloader", heroInput{servers: 1}, heroState{Statement: "Not ready to throttle", Tone: "dim", Missing: "a downloader"}},
		{"paused while playing", heroInput{paused: true, sessions: 2, servers: 1, downloaders: 2}, heroState{Statement: "Default downloader speeds enforced", Tone: "plain"}},
		{"paused, nothing playing", heroInput{paused: true, servers: 1, downloaders: 2}, heroState{Statement: "Default downloader speeds enforced", Tone: "plain", Reason: "Nothing mapped is playing, so nothing would be throttled right now in any case."}},
		{"two throttled", heroInput{throttled: []string{"qBittorrent", "Transmission"}, servers: 1, downloaders: 2}, heroState{Statement: "qBittorrent and Transmission throttled", Tone: "warn"}},
		{"three throttled", heroInput{throttled: []string{"A", "B", "C"}, servers: 1, downloaders: 3}, heroState{Statement: "A, B and 1 other throttled", Tone: "warn"}},
		{"five throttled", heroInput{throttled: []string{"A", "B", "C", "D", "E"}, servers: 1, downloaders: 5}, heroState{Statement: "A, B and 3 others throttled", Tone: "warn"}},
		{"an episode just ended", heroInput{throttled: []string{"qBittorrent"}, releasing: true, servers: 1, downloaders: 1}, heroState{Statement: "qBittorrent throttled", Tone: "warn", Reason: "An episode just ended. Default rates return in a moment, unless the next one starts."}},
		{"releasing one while another server plays", heroInput{throttled: []string{"qBittorrent"}, releasing: true, sessions: 1, servers: 2, downloaders: 2}, heroState{Statement: "qBittorrent throttled", Tone: "warn"}},
		{"just booted", heroInput{unresolved: true, servers: 1, downloaders: 2}, heroState{Statement: "State not resolved yet", Tone: "dim", Reason: "Nothing mapped is playing."}},
		{"idle", heroInput{servers: 1, downloaders: 2}, heroState{Statement: "Nothing is throttled", Tone: "good", Reason: "All downloaders at default rates."}},
	}
	for _, c := range cases {
		if got := buildHero(c.in); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

// Setup incomplete is the full Dashboard, without the pause control.
func TestDashboardSetupIncomplete(t *testing.T) {
	app := newApp(t)
	app.downloader("qBit")
	body := app.get("/").Body.String()
	for _, want := range []string{"Not ready to throttle", "Add a media server to start throttling.", "No media servers added yet", "Setup incomplete", "Throttle History"} {
		if !strings.Contains(body, want) {
			t.Errorf("setup-incomplete Dashboard is missing %q", want)
		}
	}
	if strings.Contains(body, "Nothing configured yet") || strings.Contains(body, "Disable throttling") {
		t.Error("setup incomplete rendered the first-run state or the pause control")
	}
}

// While throttling the reason line is a timer, and the log tags cause → effect.
func TestDashboardTimerAndLogTags(t *testing.T) {
	app := newApp(t)
	ms := app.mediaServer("Plex", "jellyfin", "http://plex.lan")
	app.watchers.running[ms] = true
	dl := app.downloader("qBit")
	app.setMapping(ms, dl, true)
	app.sched.HandleSessionEvent(ms, clients.Session{Key: "s"}, true)
	waitFor(t, "throttling", func() bool { return app.sched.State(dl).Throttled })

	live := app.get("/dashboard/live").Body.String()
	if !strings.Contains(live, "data-throttled-since=") || !strings.Contains(live, "Session running for:") {
		t.Errorf("no session timer while throttled: %.400s", live)
	}
	logs := app.get("/logs").Body.String()
	if !strings.Contains(logs, `<span class="at-logrow-tag">Plex</span><span class="at-logrow-arrow" aria-label="throttles">→</span><span class="at-logrow-tag">qBit</span>`) {
		t.Error("the throttle event does not read Plex → qBit")
	}
	if card := app.get("/dashboard/history?range=30d").Body.String(); !strings.Contains(card, `data-range="30d"`) {
		t.Error("the history card does not honour ?range=30d")
	}
}

func TestTabTitles(t *testing.T) {
	app := newApp(t)
	for path, want := range map[string]string{
		"/":                           "<title>AutoThrottle</title>",
		"/configuration":              "<title>Configuration</title>",
		"/configuration?tab=mappings": "<title>Configuration</title>",
		"/logs":                       "<title>Logs</title>",
		"/no-such-page":               "<title>Page not found</title>",
	} {
		expectBody(t, app.get(path), want)
	}
	signedOut := newAppWith(t, map[string]string{"AUTH_USERNAME": testUser, "AUTH_PASSWORD": testPass})
	expectBody(t, signedOut.get("/login"), "<title>Log in</title>")
	fresh := newAppWith(t, nil)
	expectBody(t, fresh.get("/setup"), "<title>Set up</title>")
}

// An unknown address gets the 404 signed in or not; a real page asks for
// a sign-in first.
func TestNotFoundPage(t *testing.T) {
	app := newApp(t)
	rec := app.get("/no-such-page")
	expectStatus(t, rec, http.StatusNotFound)
	for _, want := range []string{"Nothing here to throttle", "no route for GET /no-such-page", ">WARN<", `href="/">Go to Dashboard`, `href="/logs">Open Logs`} {
		expectBody(t, rec, want)
	}
	if strings.Contains(rec.Body.String(), "at-shell") || strings.Contains(rec.Body.String(), ">Log in<") {
		t.Error("the signed-in 404 has the app shell or a Log in button")
	}
	expectStatus(t, app.post("/no-such-page", nil), http.StatusNotFound)

	signedOut := newAppWith(t, map[string]string{"AUTH_USERNAME": testUser, "AUTH_PASSWORD": testPass})
	rec = signedOut.get("/config/servers/9?x=1")
	expectStatus(t, rec, http.StatusNotFound)
	expectBody(t, rec, "no route for GET /config/servers/9")
	expectBody(t, rec, `href="/login?next=%2Fconfig%2Fservers%2F9%3Fx%3D1">Log in`)
	if strings.Contains(rec.Body.String(), "Open Logs") {
		t.Error("the signed-out 404 offers Logs")
	}
	expectRedirect(t, signedOut.get("/logs"), "/login?next=%2Flogs")
}

// Signing in returns to the page asked for, and only ever one of this app.
func TestLoginReturnsToTheRequestedPage(t *testing.T) {
	app := newAppWith(t, map[string]string{"AUTH_USERNAME": testUser, "AUTH_PASSWORD": testPass})
	expectBody(t, app.get("/login?next=%2Flogs%3Flevel%3Dwarn"), `name="next" value="/logs?level=warn"`)
	if strings.Contains(app.get("/login?next=https://evil.example").Body.String(), `name="next"`) {
		t.Error("the login page carried an outside address")
	}
	rec := app.do("POST", "/login", url.Values{"username": {testUser}, "password": {"wrong"}, "next": {"/logs"}})
	expectBody(t, rec, `name="next" value="/logs"`)

	rec = app.do("POST", "/login", url.Values{"username": {testUser}, "password": {testPass}, "next": {"/logs?level=warn"}})
	expectRedirect(t, rec, "/logs?level=warn")
	rec = app.do("POST", "/login", url.Values{"username": {testUser}, "password": {testPass}, "next": {"//evil.example"}})
	expectRedirect(t, rec, "/")
}
