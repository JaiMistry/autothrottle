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
	Title         string
	Active        string
	Authenticated bool
	CSRFToken     string
	Error         string
}

type dashboardMediaServerView struct {
	Name           string
	Enabled        bool
	ActiveSessions int
}

type dashboardDownloaderView struct {
	Name       string
	Enabled    bool
	Throttled  bool
	StateKnown bool
}

type dashboardPageData struct {
	pageData
	HasMediaServers  bool
	MediaServerCount int
	DownloaderCount  int
	MappingCount     int
	MediaServers     []dashboardMediaServerView
	Downloaders      []dashboardDownloaderView
	Paused           bool
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

	msViews := make([]dashboardMediaServerView, 0, len(mediaServers))
	for _, ms := range mediaServers {
		msViews = append(msViews, dashboardMediaServerView{
			Name:           ms.Name,
			Enabled:        ms.Enabled,
			ActiveSessions: s.scheduler.ActiveSessionCount(ms.ID),
		})
	}

	dlViews := make([]dashboardDownloaderView, 0, len(downloaders))
	for _, dl := range downloaders {
		throttled, known := s.scheduler.DownloaderThrottled(dl.ID)
		dlViews = append(dlViews, dashboardDownloaderView{
			Name:       dl.Name,
			Enabled:    dl.Enabled,
			Throttled:  throttled,
			StateKnown: known,
		})
	}

	sessionToken := auth.SessionTokenFromContext(r.Context())

	data := dashboardPageData{
		pageData: pageData{
			Title:         "Dashboard",
			Active:        "dashboard",
			Authenticated: true,
			CSRFToken:     s.csrf.TokenFor(sessionToken),
		},
		HasMediaServers:  len(mediaServers) > 0,
		MediaServerCount: len(mediaServers),
		DownloaderCount:  len(downloaders),
		MappingCount:     len(mappings),
		MediaServers:     msViews,
		Downloaders:      dlViews,
		Paused:           s.scheduler.Paused(),
	}
	if err := s.templates["dashboard"].ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("rendering dashboard page", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) togglePause(w http.ResponseWriter, r *http.Request) {
	s.scheduler.SetPaused(r.Context(), !s.scheduler.Paused())
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
