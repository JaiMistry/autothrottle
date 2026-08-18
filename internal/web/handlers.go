package web

import (
	"log/slog"
	"net/http"

	"github.com/JaiMistry/autothrottle/internal/auth"
)

// pageData is passed to every template. Not every field applies to
// every page (e.g. Error only applies to login) — templates just don't
// render fields they don't use.
type pageData struct {
	Title            string
	Active           string
	Authenticated    bool
	CSRFToken        string
	Error            string
	HasMediaServers  bool
	MediaServerCount int
	DownloaderCount  int
	MappingCount     int
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	data := pageData{Title: "Log in"}
	if err := s.templates["login"].ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("rendering login page", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) dashboardPage(w http.ResponseWriter, r *http.Request) {
	mediaServers, err := s.db.ListMediaServers()
	if err != nil {
		slog.Error("listing media servers", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	downloaders, err := s.db.ListDownloaders()
	if err != nil {
		slog.Error("listing downloaders", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	mappings, err := s.db.ListMappings()
	if err != nil {
		slog.Error("listing mappings", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	sessionToken := auth.SessionTokenFromContext(r.Context())

	data := pageData{
		Title:            "Dashboard",
		Active:           "dashboard",
		Authenticated:    true,
		CSRFToken:        s.csrf.TokenFor(sessionToken),
		HasMediaServers:  len(mediaServers) > 0,
		MediaServerCount: len(mediaServers),
		DownloaderCount:  len(downloaders),
		MappingCount:     len(mappings),
	}
	if err := s.templates["dashboard"].ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("rendering dashboard page", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
