package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestProtectedPagesRedirectToLogin(t *testing.T) {
	app := newAppWith(t, map[string]string{"AUTH_USERNAME": testUser, "AUTH_PASSWORD": testPass})
	expectRedirect(t, app.get("/"), "/login")
	// Signing in returns to the page asked for.
	for _, path := range []string{"/configuration", "/logs?level=warn", "/dashboard/live", "/dashboard/history"} {
		expectRedirect(t, app.get(path), "/login?next="+url.QueryEscape(path))
	}
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

	app.restart() // a restart keeps you signed in

	expectStatus(t, app.get("/"), http.StatusOK)

	app.csrf = csrfField.FindStringSubmatch(app.get("/configuration").Body.String())[1]
	expectRedirect(t, app.post("/logout", nil), "/login")
	expectRedirect(t, app.get("/"), "/login")
}

// setupForm loads the create-account screen and fills in its CSRF field.
func (a *testApp) setupForm(user, pass string) url.Values {
	a.t.Helper()
	m := csrfField.FindStringSubmatch(a.get("/setup").Body.String())
	if m == nil {
		a.t.Fatal("the create-account screen has no CSRF field")
	}
	return url.Values{"username": {user}, "password": {pass}, "csrf_token": {m[1]}}
}

func TestSetupIsRateLimited(t *testing.T) {
	app := newAppWith(t, nil)
	var last int
	for i := 0; i < 8; i++ {
		last = app.do("POST", "/setup", app.setupForm(" ", "")).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after 8 rapid attempts: %d, want 429", last)
	}
	rec := app.do("POST", "/setup", app.setupForm("jai", "pw"))
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
	expectRedirect(t, app.get("/login"), "/")
}

func TestFirstRunSetup(t *testing.T) {
	app := newAppWith(t, nil)

	// Every address leads to setup, made-up ones included.
	for _, path := range []string{"/", "/login", "/logs", "/no-such-page"} {
		expectRedirect(t, app.get(path), "/setup")
	}
	page := app.get("/setup")
	expectStatus(t, page, http.StatusOK)
	for _, want := range []string{"Set up your login", "Create your login credentials for access.", `autocomplete="new-password"`, "data-reveal", ">Create account<"} {
		expectBody(t, page, want)
	}
	if strings.Contains(page.Body.String(), `name="confirm"`) || strings.Contains(page.Body.String(), "minlength") {
		t.Error("the screen still asks for a confirmation or sets a length")
	}

	// A cross-site post has no way to the token.
	forged := app.do("POST", "/setup", url.Values{"username": {"intruder"}, "password": {"pw"}})
	expectStatus(t, forged, http.StatusForbidden)
	if !app.srv.auth.NeedsSetup() {
		t.Fatal("a forged post created the account")
	}

	expectStatus(t, app.do("POST", "/setup", app.setupForm("jai", "")), http.StatusUnprocessableEntity)

	// No password rules: one character is a password.
	rec := app.do("POST", "/setup", app.setupForm("jai", "x"))
	expectRedirect(t, rec, "/")
	expectBody(t, app.get("/"), "Nothing configured yet")

	// Then the screen and its address are gone for good.
	expectStatus(t, app.get("/setup"), http.StatusNotFound)
	expectStatus(t, app.do("POST", "/setup", url.Values{"username": {"intruder"}, "password": {"pw"}}), http.StatusNotFound)
	if !app.srv.auth.Verify("jai", "x") || app.srv.auth.Verify("intruder", "pw") {
		t.Error("the account changed after setup")
	}
}
