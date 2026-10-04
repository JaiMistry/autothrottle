package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Every page behind login, as a visitor without a session sees it.
func TestProtectedPagesRedirectToLogin(t *testing.T) {
	app := newAppWith(t, map[string]string{"AUTH_USERNAME": testUser, "AUTH_PASSWORD": testPass})
	for _, path := range []string{"/", "/configuration", "/logs", "/dashboard/live", "/activity"} {
		expectRedirect(t, app.get(path), "/login")
	}
	// A state change without a session must not happen either.
	expectRedirect(t, app.do("POST", "/pause-toggle", url.Values{}), "/login")
	if app.sched.Paused() {
		t.Error("an anonymous POST paused throttling")
	}
}

func TestLogin(t *testing.T) {
	app := newAppWith(t, map[string]string{"AUTH_USERNAME": testUser, "AUTH_PASSWORD": testPass})

	rec := app.do("POST", "/login", url.Values{"username": {testUser}, "password": {"wrong"}})
	expectStatus(t, rec, http.StatusUnauthorized)
	// Generic on purpose: naming the wrong half would confirm the username.
	expectBody(t, rec, "Invalid username or password.")

	rec = app.do("POST", "/login", url.Values{"username": {testUser}, "password": {testPass}})
	expectRedirect(t, rec, "/")
	cookie := rec.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.MaxAge != 24*60*60 {
		t.Errorf("session cookie = %+v, want HttpOnly with a 24h lifetime", cookie)
	}
	expectStatus(t, app.get("/"), http.StatusOK)

	// A restart (a new server over the same database) keeps you signed in.
	app.restart()
	expectStatus(t, app.get("/"), http.StatusOK)

	app.csrf = csrfField.FindStringSubmatch(app.get("/configuration").Body.String())[1]
	expectRedirect(t, app.post("/logout", nil), "/login")
	expectRedirect(t, app.get("/"), "/login")
}

// The setup form is the one page open to anyone, so it is throttled
// like the login form.
func TestSetupIsRateLimited(t *testing.T) {
	app := newAppWith(t, nil)
	var last int
	for i := 0; i < 8; i++ {
		last = app.do("POST", "/setup", url.Values{"username": {"x"}, "password": {"short"}, "confirm": {"short"}}).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after 8 rapid attempts: %d, want 429", last)
	}
	rec := app.do("POST", "/setup", url.Values{"username": {"jai"}, "password": {"long-enough"}, "confirm": {"long-enough"}})
	expectStatus(t, rec, http.StatusTooManyRequests)
	if !app.srv.auth.NeedsSetup() {
		t.Error("a rate-limited attempt created the account")
	}
}

func TestLoginIsRateLimited(t *testing.T) {
	app := newAppWith(t, map[string]string{"AUTH_USERNAME": testUser, "AUTH_PASSWORD": testPass})
	var last int
	for i := 0; i < 8; i++ {
		last = app.do("POST", "/login", url.Values{"username": {testUser}, "password": {"guess"}}).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after 8 rapid attempts: %d, want 429", last)
	}
	// Even the right password waits out the window.
	rec := app.do("POST", "/login", url.Values{"username": {testUser}, "password": {testPass}})
	expectStatus(t, rec, http.StatusTooManyRequests)
}

func TestStateChangesNeedTheCSRFToken(t *testing.T) {
	app := newApp(t)
	rec := app.do("POST", "/pause-toggle", url.Values{"csrf_token": {"forged"}})
	expectStatus(t, rec, http.StatusForbidden)
	if app.sched.Paused() {
		t.Error("a forged request paused throttling")
	}
	app.post("/pause-toggle", nil)
	if !app.sched.Paused() {
		t.Error("a genuine request did not pause throttling")
	}
}

func TestDisabledAuthLetsEveryoneIn(t *testing.T) {
	app := newAppWith(t, map[string]string{"AUTH_DISABLED": "true"})
	expectStatus(t, app.get("/"), http.StatusOK)
	// Nothing to log in to, so the login page steps aside.
	expectRedirect(t, app.get("/login"), "/")
}

func TestFirstRunSetup(t *testing.T) {
	app := newAppWith(t, nil)

	// Nothing can log in yet, so every road leads to setup.
	expectRedirect(t, app.get("/"), "/setup")
	expectRedirect(t, app.get("/login"), "/setup")
	expectStatus(t, app.get("/setup"), http.StatusOK)

	type result struct {
		code           int
		location, body string
	}
	submit := func(user, pass, confirm string) result {
		rec := app.do("POST", "/setup", url.Values{"username": {user}, "password": {pass}, "confirm": {confirm}})
		return result{rec.Code, rec.Header().Get("Location"), rec.Body.String()}
	}

	r := submit("jai", "long-enough", "different")
	if r.code != http.StatusUnprocessableEntity || !strings.Contains(r.body, "two passwords") {
		t.Fatalf("mismatched passwords: %d %.200s", r.code, r.body)
	}
	r = submit("jai", "short", "short")
	if r.code != http.StatusUnprocessableEntity || !strings.Contains(r.body, "at least 8 characters") {
		t.Fatalf("short password: %d %.200s", r.code, r.body)
	}

	r = submit("jai", "long-enough", "long-enough")
	if r.code != http.StatusSeeOther || r.location != "/" {
		t.Fatalf("valid setup: %d → %q", r.code, r.location)
	}
	// Setup signs you straight in.
	expectStatus(t, app.get("/"), http.StatusOK)
	// And can never run again.
	expectRedirect(t, app.get("/setup"), "/login")
	r = submit("intruder", "long-enough", "long-enough")
	if r.location != "/login" {
		t.Fatalf("second setup was not refused: %d → %q", r.code, r.location)
	}
}
