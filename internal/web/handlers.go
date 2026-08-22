package web

import (
	"log/slog"
	"net/http"

	"github.com/JaiMistry/autothrottle/internal/version"
)

// pageData is passed to every template. Not every field applies to
// every page (e.g. Error only applies to login) — templates just don't
// render fields they don't use.
type pageData struct {
	Title         string
	Active        string
	Authenticated bool
	CSRFToken     string
	Error         string
	Version       string
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	data := pageData{Title: "Log in", Version: version.Version}
	if err := s.templates["login"].ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("rendering login page", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
