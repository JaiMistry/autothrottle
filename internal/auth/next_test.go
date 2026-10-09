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
		hx             bool
		want           string
	}{
		{"GET", "/logs?level=warn", false, "/login?next=%2Flogs%3Flevel%3Dwarn"},
		{"GET", "/", false, "/login"},
		{"GET", "/events", false, "/login"},
		{"GET", "/dashboard/live", true, "/login"},
		{"POST", "/pause-toggle", false, "/login"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.target, nil)
		if c.hx {
			r.Header.Set("HX-Request", "true")
		}
		if got := loginURL(r); got != c.want {
			t.Errorf("%s %s (hx %v) → %q, want %q", c.method, c.target, c.hx, got, c.want)
		}
	}
}
