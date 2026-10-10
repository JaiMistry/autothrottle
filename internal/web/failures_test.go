package web

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/version"
)

// breakDB runs a statement on a second connection, to take away the one
// table or column a query needs while the rest keep working.
func (a *testApp) breakDB(stmt string) {
	a.t.Helper()
	conn, err := sql.Open("sqlite", a.dbPath)
	if err != nil {
		a.t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Exec(stmt); err != nil {
		a.t.Fatalf("%s: %v", stmt, err)
	}
}

// openApp is an instance with sign-in off, so a failing database reaches
// the page rather than failing the session check first.
func openApp(t *testing.T) *testApp {
	return newAppWith(t, map[string]string{"AUTH_DISABLED": "true"})
}

func TestDashboardRowStates(t *testing.T) {
	app := newApp(t)
	dropped := app.mediaServer("Dropped", "jellyfin", "http://jf.lan")
	app.watchers.running[dropped], app.watchers.dropped[dropped] = true, true
	_, _ = app.db.CreateMediaServer(db.MediaServer{Name: "Off", Type: "emby", URL: "http://emby.lan"})
	_, _ = app.db.CreateDownloader(db.Downloader{Name: "Off", Type: "transmission", URL: "http://tr.lan"})
	failing, _ := app.db.CreateDownloader(db.Downloader{Name: "Gone", Type: "qbittorrent", URL: "http://127.0.0.1:1", Enabled: true})
	app.sched.ResolveAsync()
	waitFor(t, "the failed apply", func() bool { return app.sched.State(failing).Failure != "" })

	live := app.get("/dashboard/live").Body.String()
	servers, downloaders := strings.Join(rowStates(live, "at-serverrow"), " "), strings.Join(rowStates(live, "at-dlrow-wrap"), " ")
	for _, want := range []string{"connecting", "disabled"} {
		if !strings.Contains(servers, want) {
			t.Errorf("server rows %q have no %s row", servers, want)
		}
	}
	for _, want := range []string{"unreachable", "disabled"} {
		if !strings.Contains(downloaders, want) {
			t.Errorf("downloader rows %q have no %s row", downloaders, want)
		}
	}
}

func TestLocalPlaybackFootnote(t *testing.T) {
	app := newApp(t)
	ms := app.mediaServer("Jelly", "jellyfin", "http://jf.lan")
	app.watchers.running[ms] = true
	app.sched.HandleSessionEvent(ms, clients.Session{Key: "s", Title: "Andor", Local: true}, true)
	expectBody(t, app.get("/dashboard/live"), "includes local playback")
}

// A session counted before any snapshot described it is named from the
// session history instead.
func TestPlayingDetailFromHistory(t *testing.T) {
	app := newApp(t)
	ms := app.mediaServer("Jelly", "jellyfin", "http://jf.lan")
	app.watchers.running[ms] = true
	for _, key := range []string{"a", "b", "c", "d"} {
		app.sched.HandleSessionEvent(ms, clients.Session{Key: key}, true)
	}
	expectBody(t, app.get("/dashboard/live"), "4 sessions counting toward throttling")

	for key, detail := range map[string][2]string{"a": {"Andor", "TV"}, "b": {"Andor", "iPad"}, "c": {"Severance", ""}, "d": {"", "Phone"}} {
		if err := app.db.OpenSession(ms, key, detail[0], detail[1]); err != nil {
			t.Fatal(err)
		}
	}
	// Sessions sharing a title share a line.
	rec := app.get("/dashboard/live")
	if !strings.Contains(rec.Body.String(), "Andor on TV · iPad") && !strings.Contains(rec.Body.String(), "Andor on iPad · TV") {
		t.Errorf("one title on two devices is not one line: %.300s", rec.Body.String())
	}
	for _, want := range []string{"Severance", "Phone"} {
		expectBody(t, rec, want)
	}

	app.breakDB(`ALTER TABLE session_history RENAME TO gone`)
	expectBody(t, app.get("/dashboard/live"), "4 sessions counting toward throttling")
}

func TestDashboardOnAFailingDatabase(t *testing.T) {
	app := openApp(t)
	app.breakDB(`ALTER TABLE throttle_periods RENAME TO gone`)
	expectStatus(t, app.get("/"), http.StatusInternalServerError)
	expectStatus(t, app.get("/dashboard/history"), http.StatusInternalServerError)

	app = openApp(t)
	app.breakDB(`ALTER TABLE runs RENAME TO gone`)
	expectStatus(t, app.get("/dashboard/history"), http.StatusInternalServerError)

	app = openApp(t)
	app.breakDB(`ALTER TABLE downloaders RENAME TO gone`)
	expectStatus(t, app.get("/dashboard/live"), http.StatusInternalServerError)
	expectStatus(t, app.get("/configuration"), http.StatusInternalServerError)

	app = openApp(t)
	app.breakDB(`ALTER TABLE mappings RENAME TO gone`)
	expectStatus(t, app.get("/configuration"), http.StatusInternalServerError)

	app = openApp(t)
	_ = app.db.Close()
	expectStatus(t, app.get("/dashboard/live"), http.StatusInternalServerError)
}

func TestLogsOnAFailingDatabase(t *testing.T) {
	app := openApp(t)
	app.breakDB(`ALTER TABLE events RENAME COLUMN downloader_id TO gone`)
	expectStatus(t, app.get("/logs"), http.StatusInternalServerError)

	app = openApp(t)
	app.breakDB(`ALTER TABLE media_servers RENAME TO gone`)
	expectStatus(t, app.get("/logs"), http.StatusInternalServerError)

	app = openApp(t)
	app.breakDB(`ALTER TABLE downloaders RENAME TO gone`)
	expectStatus(t, app.get("/logs"), http.StatusInternalServerError)

	app = openApp(t)
	_ = app.db.Close()
	for _, path := range []string{"/logs", "/logs/panel", "/logs/rows"} {
		expectStatus(t, app.get(path), http.StatusInternalServerError)
	}
	expectStatus(t, app.post("/logs/clear", nil), http.StatusInternalServerError)
}

func TestLogRowsFragment(t *testing.T) {
	app := newApp(t)
	app.events(3, "info")
	// A time the page cannot read is shown as stored.
	if err := app.db.InsertEvent(db.Event{Level: "warn", Message: "odd", OccurredAt: "sometime"}); err != nil {
		t.Fatal(err)
	}
	rec := app.get("/logs/rows?per=nonsense")
	expectStatus(t, rec, http.StatusOK)
	expectBody(t, rec, "sometime")
	if n := countRows(rec.Body.String()); n != 4 {
		t.Errorf("%d rows, want 4", n)
	}
}

func TestSetupFailures(t *testing.T) {
	// The account cannot be saved.
	app := newAppWith(t, nil)
	form := app.setupForm("owner", "a-long-enough-password")
	app.breakDB(`ALTER TABLE account RENAME TO gone`)
	rec := app.post("/setup", form)
	expectStatus(t, rec, http.StatusInternalServerError)
	expectBody(t, rec, "could not be saved")

	// Saved, but the automatic sign-in failed: off to sign in by hand.
	app = newAppWith(t, nil)
	form = app.setupForm("owner", "a-long-enough-password")
	app.breakDB(`ALTER TABLE sessions RENAME TO gone`)
	expectRedirect(t, app.post("/setup", form), "/login")
}

func TestStaticAssetCaching(t *testing.T) {
	app := newApp(t)
	versioned := app.srv.assets.URL("/static/js/app.js")
	if !strings.Contains(versioned, "?v=") {
		t.Fatalf("no version on %q", versioned)
	}
	rec := app.get(versioned)
	expectStatus(t, rec, http.StatusOK)
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("versioned asset cached as %q", cc)
	}
	etag := rec.Header().Get("ETag")

	rec = app.get("/static/js/app.js")
	if cc := rec.Header().Get("Cache-Control"); cc != "public, no-cache" {
		t.Errorf("unversioned asset cached as %q", cc)
	}

	req := httptest.NewRequest("GET", "/static/js/app.js", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	app.handler.ServeHTTP(rec, req)
	expectStatus(t, rec, http.StatusNotModified)

	if got := app.srv.assets.URL("/static/missing.css"); got != "/static/missing.css" {
		t.Errorf("an unknown asset got %q", got)
	}
}

// A stream sends keep-alives while idle and ends at its lifetime, so the
// browser's reconnect passes the sign-in check again.
func TestEventStreamHeartbeatAndLifetime(t *testing.T) {
	beat, lifetime := streamHeartbeat, streamLifetime
	streamHeartbeat, streamLifetime = 50*time.Millisecond, 400*time.Millisecond
	t.Cleanup(func() { streamHeartbeat, streamLifetime = beat, lifetime })

	app := newApp(t)
	st := app.openStream()
	st.expect(": keep-alive")
	done := make(chan struct{})
	go func() {
		for range st.lines {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the stream outlived its lifetime")
	}
}

// Without a flushable connection there is no stream to serve.
func TestEventStreamNeedsStreaming(t *testing.T) {
	app := newApp(t)
	rec := httptest.NewRecorder()
	app.srv.liveEvents(struct{ http.ResponseWriter }{rec}, httptest.NewRequest("GET", "/events", nil))
	expectStatus(t, rec, http.StatusInternalServerError)
}

func TestUpdateCheckFailures(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"server error":  func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) },
		"not JSON":      func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) },
		"no tag":        func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"tag_name":""}`)) },
		"no connection": nil,
	} {
		t.Run(name, func(t *testing.T) {
			old := releasesAPI
			t.Cleanup(func() { releasesAPI = old })
			releasesAPI = "http://127.0.0.1:1"
			if handler != nil {
				gh := httptest.NewServer(handler)
				t.Cleanup(gh.Close)
				releasesAPI = gh.URL
			}
			if view := checkLatestRelease(t.Context(), "1.0.0"); view.State != "failed" {
				t.Errorf("state %q", view.State)
			}
		})
	}
	old := releasesAPI
	t.Cleanup(func() { releasesAPI = old })
	releasesAPI = "http://bad host"
	if view := checkLatestRelease(t.Context(), "1.0.0"); view.State != "failed" {
		t.Errorf("an unusable address: state %q", view.State)
	}
}

// The button's request, end to end.
func TestUpdateCheckEndpoint(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
	}))
	t.Cleanup(gh.Close)
	old, oldVersion := releasesAPI, version.Version
	t.Cleanup(func() { releasesAPI, version.Version = old, oldVersion })
	releasesAPI, version.Version = gh.URL, "1.0.0"

	app := newApp(t)
	expectBody(t, app.get("/configuration/update-check"), "v9.9.9 available")
	if got := installedLabel("v1.0.0"); got != "v1.0.0 installed" {
		t.Errorf("installedLabel = %q", got)
	}
}

func TestAboutReadsTheBuild(t *testing.T) {
	old := version.BuildDate
	t.Cleanup(func() { version.BuildDate = old })
	version.BuildDate = "2026-10-01T12:00:00Z"
	app := newApp(t)
	if note := app.srv.aboutView().BuiltNote; !strings.HasPrefix(note, "Built: ") {
		t.Errorf("built note %q", note)
	}
	if got := uptimeSince(time.Now().Add(time.Hour)); got != "0d : 00h : 00m : 00s" {
		t.Errorf("a boot in the future reads %q", got)
	}
}

func TestSmallHelpers(t *testing.T) {
	if _, err := dict("odd"); err == nil {
		t.Error("dict took an odd number of arguments")
	}
	if _, err := dict(1, 2); err == nil {
		t.Error("dict took a key that is not a string")
	}
	if got := filterEmptyNote("warn", ""); got != "No warn events." {
		t.Errorf("filterEmptyNote = %q", got)
	}
	if got := stepOf(120); got != len(throttleSteps)-1 {
		t.Errorf("two hours is step %d", got)
	}
	for d, want := range map[time.Duration]string{-time.Second: "0:00", 75 * time.Minute: "1:15:00"} {
		if got := timecode(d); got != want {
			t.Errorf("timecode(%v) = %q, want %q", d, got, want)
		}
	}
	future := clients.Session{Position: time.Minute, PositionAt: time.Now().Add(time.Hour)}
	if got := livePosition(future, time.Now()); got != time.Minute {
		t.Errorf("a reading from the future moved to %v", got)
	}
}

func TestRenderingAnUnknownTemplateFails(t *testing.T) {
	app := newApp(t)
	rec := httptest.NewRecorder()
	app.srv.renderFragment(rec, "dashboard", "no-such-template", nil)
	expectStatus(t, rec, http.StatusInternalServerError)
}

func TestOldHistoryLinkGoesToLogs(t *testing.T) {
	app := newApp(t)
	rec := app.get("/history")
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/logs" {
		t.Errorf("%d → %q", rec.Code, rec.Header().Get("Location"))
	}
}

// A 404 is still served when it cannot be logged.
func TestNotFoundWithoutALog(t *testing.T) {
	app := openApp(t)
	_ = app.db.Close()
	expectStatus(t, app.get("/no-such-page"), http.StatusNotFound)
}

func TestLoginFailures(t *testing.T) {
	app := newAppWith(t, map[string]string{"AUTH_USERNAME": testUser, "AUTH_PASSWORD": testPass})
	rec := app.do("POST", "/login", url.Values{"username": {testUser}})
	expectStatus(t, rec, http.StatusUnauthorized)
	expectBody(t, rec, "Enter a username and password.")

	// The password is right, but no session can be recorded.
	app.breakDB(`ALTER TABLE sessions RENAME TO gone`)
	expectStatus(t, app.do("POST", "/login", url.Values{"username": {testUser}, "password": {testPass}}), http.StatusInternalServerError)
}

// A new entry is saved even when its mappings cannot be seeded; that is
// logged, and the mappings can still be ticked by hand.
func TestSaveSurvivesUnseededMappings(t *testing.T) {
	app := newApp(t)
	app.breakDB(`CREATE TRIGGER refuse BEFORE INSERT ON mappings BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	app.mediaServer("Jelly", "jellyfin", "http://jf.lan")
	app.downloader("qBit")
	expectRedirect(t, app.post("/configuration/media-servers", mediaServerForm("Emby", "emby", "http://emby.lan", "k")), "/configuration?tab=connections")
	expectRedirect(t, app.post("/configuration/downloaders", url.Values{"name": {"Tr"}, "type": {"transmission"}, "url": {"http://tr.lan"}}), "/configuration?tab=connections")
	servers, _ := app.db.ListMediaServers()
	downloaders, _ := app.db.ListDownloaders()
	if len(servers) != 2 || len(downloaders) != 2 {
		t.Errorf("%d servers, %d downloaders", len(servers), len(downloaders))
	}
}

// Before an account exists every page leads to the create-account screen.
func TestPagesLeadToSetup(t *testing.T) {
	app := newAppWith(t, nil)
	expectRedirect(t, app.get("/"), "/setup")
}

// The saved password is used for a test from the edit form, and a
// failing saved client says so.
func TestDownloaderTestsWithTheSavedPassword(t *testing.T) {
	app := newApp(t)
	form := url.Values{"name": {"qBit"}, "type": {"qbittorrent"}, "url": {app.qbit.URL}, "username": {"admin"}, "password": {"secret"}}
	expectRedirect(t, app.post("/configuration/downloaders", form), "/configuration?tab=connections")
	dls, _ := app.db.ListDownloaders()
	form.Set("id", fmt.Sprint(dls[0].ID))
	form.Del("password")
	expectBody(t, app.post("/configuration/downloaders/test", form), "Successfully connected.")

	gone, _ := app.db.CreateDownloader(db.Downloader{Name: "Gone", Type: "qbittorrent", URL: "http://127.0.0.1:1"})
	expectBody(t, app.post(fmt.Sprintf("/configuration/downloaders/%d/test", gone), nil), "Failed")
}

// The hour in progress reads as up while this run's heartbeat is recent,
// though the last one recorded can be a minute old.
func TestHistoryCountsTheRunInProgress(t *testing.T) {
	now := time.Now()
	if now.Minute() < 2 || now.Hour() == 23 {
		t.Skip("needs two minutes into an hour that is not the day's last")
	}
	app := newApp(t)
	if _, err := app.db.StartRun(now.Add(-90 * time.Second)); err != nil {
		t.Fatal(err)
	}
	view, err := app.srv.loadHistory(7)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range view.Rows {
		for i, cell := range row.Cells {
			if cell.Future && i > 0 {
				if row.Cells[i-1].Value == "AutoThrottle was off" {
					t.Error("the hour in progress reads as off")
				}
				return
			}
		}
	}
	t.Fatal("no hour in progress found")
}
