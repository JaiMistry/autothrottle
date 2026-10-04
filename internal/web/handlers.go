package web

import (
	"net/http"

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
}

// loginFailed re-renders the login page carrying the reason, so a
// rejected attempt looks like the app rather than like a server error.
func (s *Server) loginFailed(w http.ResponseWriter, r *http.Request, status int, message string) {
	w.WriteHeader(status)
	data := pageData{Title: "Log in", Version: version.Label(), Error: message, Asset: s.assets.URL}
	s.render(w, "login", data)
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	switch {
	case s.auth.Disabled():
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	case s.auth.NeedsSetup():
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	data := pageData{Title: "Log in", Version: version.Label(), Asset: s.assets.URL}
	s.render(w, "login", data)
}
