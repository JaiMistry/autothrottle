package web

import (
	"net/http"
	"net/url"
	"time"

	"github.com/JaiMistry/autothrottle/internal/auth"
	"github.com/JaiMistry/autothrottle/internal/version"
)

// pageData is passed to every template. Not every field applies to
// every page (e.g. Error only applies to login) — templates just don't
// render fields they don't use.
type pageData struct {
	// Asset turns a static path into its content-hashed URL, so a new
	// build is never served stale CSS from a browser cache.
	Asset         func(string) string
	Title         string
	Active        string
	Authenticated bool
	CSRFToken     string
	Error         string
	Version       string
	// Next is where signing in returns to, when it is not the Dashboard.
	Next string
}

// loginNext is the return address a login page carries: a safe one
// from the request, or nothing for the Dashboard.
func loginNext(raw string) string {
	if next := auth.SafeNext(raw); next != "/" {
		return next
	}
	return ""
}

// loginFailed re-renders the login page carrying the reason, so a
// rejected attempt looks like the app rather than like a server error.
func (s *Server) loginFailed(w http.ResponseWriter, r *http.Request, status int, message string) {
	w.WriteHeader(status)
	data := pageData{Title: "Log in", Version: version.Label(), Error: message, Asset: s.assets.URL, Next: loginNext(r.FormValue("next"))}
	s.render(w, "login", data)
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	switch {
	case s.auth.Disabled():
		http.Redirect(w, r, auth.SafeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	case s.auth.NeedsSetup():
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	data := pageData{Title: "Log in", Version: version.Label(), Asset: s.assets.URL, Next: loginNext(r.URL.Query().Get("next"))}
	s.render(w, "login", data)
}

// notFoundData is the 404 page: the request written up as a log line.
type notFoundData struct {
	pageData
	SignedIn bool
	Time     string
	Method   string
	Path     string
	LoginURL string
}

// notFound answers an address no page lives at. It is open to anyone,
// so a signed-out visitor following a dead link still gets the app's
// own page, with a way to sign in that comes back here.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	data := notFoundData{
		pageData: pageData{Title: "Page not found", Version: version.Label(), Asset: s.assets.URL},
		SignedIn: auth.Authenticated(s.auth, s.sessions, r),
		Time:     time.Now().Format("15:04:05"),
		Method:   r.Method,
		Path:     r.URL.Path,
		LoginURL: "/login?next=" + url.QueryEscape(r.URL.RequestURI()),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	s.render(w, "notfound", data)
}
