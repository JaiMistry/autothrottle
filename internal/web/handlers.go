package web

import (
	"net/http"
	"time"

	"github.com/JaiMistry/autothrottle/internal/auth"
	"github.com/JaiMistry/autothrottle/internal/version"
)

// pageData is passed to every template; each uses the fields it needs.
type pageData struct {
	// Asset turns a static path into its content-hashed URL.
	Asset         func(string) string
	Title         string
	Active        string
	Authenticated bool
	CSRFToken     string
	Error         string
	Version       string
	Next          string // where signing in returns to, if not the Dashboard
	Paused        bool   // shows the "Throttling disabled" banner
	PausedSince   string // e.g. "2:06 PM", server time
}

// loginNext is the return address a login page carries: a safe one
// from the request, or nothing for the Dashboard.
func loginNext(raw string) string {
	if next := auth.SafeNext(raw); next != "/" {
		return next
	}
	return ""
}

// loginFailed re-renders the login page with the reason.
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
	Time    string
	Message string
}

// notFound answers an address no page lives at. Only someone signed in
// sees it: anyone else goes to the sign-in page, and from there to the
// Dashboard rather than back to the dead link.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	if !auth.Authenticated(s.auth, s.sessions, r) {
		auth.RedirectToLogin(w, r)
		return
	}
	now := time.Now()
	data := notFoundData{
		pageData: pageData{Title: "Page not found", Version: version.Label(), Asset: s.assets.URL},
		Time:     now.Format("15:04:05"),
		Message:  notFoundMessage(r.Method, r.URL.Path),
	}
	s.recordNotFound(r, data.Message, now)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	s.render(w, "notfound", data)
}
