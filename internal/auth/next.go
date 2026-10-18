package auth

import (
	"net/http"
	"net/url"
	"strings"
)

// loginURL is where a request without a session goes, carrying the page
// to return to after signing in: the one asked for, or for a request
// htmx made, the one it was made from. Other requests just go to the
// login page.
func loginURL(r *http.Request) string {
	next := ""
	if r.Header.Get("HX-Request") == "true" {
		if u, err := url.Parse(r.Header.Get("HX-Current-URL")); err == nil {
			next = SafeNext(u.RequestURI())
		}
	} else if r.Method == http.MethodGet && r.URL.Path != "/events" {
		next = r.URL.RequestURI()
	}
	if next == "" || next == "/" {
		return "/login"
	}
	return "/login?next=" + url.QueryEscape(next)
}

// redirect sends a request elsewhere. htmx would follow an ordinary
// redirect itself and put the page it lands on inside the one that asked:
// the sign-in form inside the app, once a session expires. HX-Redirect
// has it load the page instead.
func redirect(w http.ResponseWriter, r *http.Request, to string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// RedirectToLogin sends someone signed out to the sign-in page, with
// nothing to come back to afterwards.
func RedirectToLogin(w http.ResponseWriter, r *http.Request) { redirect(w, r, "/login") }

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
