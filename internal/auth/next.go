package auth

import (
	"net/http"
	"net/url"
	"strings"
)

// loginURL is where a request without a session goes. A page asked for
// directly carries its address, to return to after signing in;
// background requests and non-GETs just go to the login page.
func loginURL(r *http.Request) string {
	if r.Method != http.MethodGet || r.Header.Get("HX-Request") == "true" ||
		r.URL.Path == "/" || r.URL.Path == "/events" {
		return "/login"
	}
	return "/login?next=" + url.QueryEscape(r.URL.RequestURI())
}

// SafeNext returns raw if it is an address in this app, else "/", so a
// crafted login link cannot redirect off-site.
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

// Authenticated applies RequireAuth's test, for pages open to everyone
// that show more to someone signed in.
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
