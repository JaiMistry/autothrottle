package auth

import (
	"net/http"
	"net/url"
	"strings"
)

// loginURL is where a request without a session is sent. A page asked
// for directly carries its own address along, so signing in lands on it
// rather than on the Dashboard. Background requests — HTMX refreshes,
// the event stream, anything but a GET — just go to the login page:
// they are not somewhere a person meant to arrive.
func loginURL(r *http.Request) string {
	if r.Method != http.MethodGet || r.Header.Get("HX-Request") == "true" ||
		r.URL.Path == "/" || r.URL.Path == "/events" {
		return "/login"
	}
	return "/login?next=" + url.QueryEscape(r.URL.RequestURI())
}

// SafeNext returns raw when it is an address inside this app, and "/"
// otherwise. Without this check a crafted login link could send someone
// to another site straight after they signed in.
func SafeNext(raw string) string {
	// "//host" and "/\host" are both read by browsers as another site.
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, "/\\") {
		return "/"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || strings.ContainsAny(raw, "\r\n\t") {
		return "/"
	}
	return raw
}

// Authenticated reports whether r is signed in, the same test
// RequireAuth applies, for pages open to everyone that still show more
// to someone signed in.
func Authenticated(a *Authenticator, sessions *SessionManager, r *http.Request) bool {
	if a.Disabled() {
		return true
	}
	if a.NeedsSetup() {
		return false
	}
	cookie, err := r.Cookie(cookieName)
	return err == nil && sessions.Valid(cookie.Value)
}
