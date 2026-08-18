package web

import (
	"log/slog"
	"net/http"

	"github.com/JaiMistry/autothrottle/internal/auth"
)

const historyPageLimit = 200

type historyEventView struct {
	OccurredAt string
	Level      string
	Message    string
}

type historyPageData struct {
	pageData
	Events []historyEventView
}

func (s *Server) historyPage(w http.ResponseWriter, r *http.Request) {
	events, err := s.db.ListEvents(historyPageLimit)
	if err != nil {
		slog.Error("listing events", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	views := make([]historyEventView, 0, len(events))
	for _, e := range events {
		views = append(views, historyEventView{
			OccurredAt: e.OccurredAt,
			Level:      e.Level,
			Message:    e.Message,
		})
	}

	sessionToken := auth.SessionTokenFromContext(r.Context())

	data := historyPageData{
		pageData: pageData{
			Title:         "History",
			Active:        "history",
			Authenticated: true,
			CSRFToken:     s.csrf.TokenFor(sessionToken),
		},
		Events: views,
	}
	if err := s.templates["history"].ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("rendering history page", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
