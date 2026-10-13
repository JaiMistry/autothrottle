package auth

import (
	"net/http/httptest"
	"testing"
)

func TestSafeNext(t *testing.T) {
	for raw, want := range map[string]string{
		"":                         "/",
		"/":                        "/",
		"/logs?level=warn":         "/logs?level=warn",
		"/configuration?tab=about": "/configuration?tab=about",
		"/no-such-page":            "/no-such-page",
		"//evil.example":           "/",
		"/\\evil.example":          "/",
		"https://evil.example/":    "/",
		"evil.example":             "/",
		"javascript:alert(1)":      "/",
		"/logs\r\nSet-Cookie: x":   "/",
	} {
		if got := SafeNext(raw); got != want {
			t.Errorf("SafeNext(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestLoginURL(t *testing.T) {
	cases := []struct {
		method, target string
		from           string // the page htmx asked from; "" if not htmx
		want           string
	}{
		{"GET", "/logs?level=warn", "", "/login?next=%2Flogs%3Flevel%3Dwarn"},
		{"GET", "/", "", "/login"},
		{"GET", "/events", "", "/login"},
		{"POST", "/pause-toggle", "", "/login"},
		{"GET", "/dashboard/live", "http://at.example/", "/login"},
		{"GET", "/logs/page", "http://at.example/logs?level=warn", "/login?next=%2Flogs%3Flevel%3Dwarn"},
		{"POST", "/configuration/test", "http://at.example/configuration", "/login?next=%2Fconfiguration"},
		{"GET", "/dashboard/live", "", "/login"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.target, nil)
		if c.from != "" || c.target == "/dashboard/live" {
			r.Header.Set("HX-Request", "true")
			r.Header.Set("HX-Current-URL", c.from)
		}
		if got := loginURL(r); got != c.want {
			t.Errorf("%s %s (from %q) → %q, want %q", c.method, c.target, c.from, got, c.want)
		}
	}
}

func TestAuthenticated(t *testing.T) {
	sessions := NewSessionManager(&memStore{})
	req := httptest.NewRequest("GET", "/", nil)
	disabled, _ := load(t, "true", "", "", &accountMem{})
	setup, _ := load(t, "", "", "", &accountMem{})
	if !Authenticated(disabled, sessions, req) {
		t.Error("sign-in off, and not treated as signed in")
	}
	if Authenticated(setup, sessions, req) {
		t.Error("signed in before an account exists")
	}
}
