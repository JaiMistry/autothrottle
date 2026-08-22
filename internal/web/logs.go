package web

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/JaiMistry/autothrottle/internal/db"
)

// logsWindow is how far back the page looks. The header says so
// explicitly ("34 of the last 200 events") rather than implying the
// list is everything that ever happened.
const logsWindow = 200

const logsPerPageDefault = 20

type logsData struct {
	pageData
	Rows logRowsData
}

// logRowsData is the bounded live region: one card body, one template
// fragment, one HTMX swap. Paging and filtering replace only this.
type logRowsData struct {
	Events    []logRowView
	Level     string
	Search    string
	PerPage   int
	Page      int
	Pages     int
	Total     int
	Window    int
	RangeFrom int
	RangeTo   int
	Counts    map[string]int
	CountAll  int
	HasPrev   bool
	HasNext   bool
	PrevPage  int
	NextPage  int
}

func (s *Server) logsPage(w http.ResponseWriter, r *http.Request) {
	rows, err := s.logRows(r)
	if err != nil {
		slog.Error("listing events", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.render(w, "logs", logsData{pageData: s.page(r, "Logs", "logs"), Rows: rows})
}

// logRowsFragment serves the same block on its own, for paging and
// filtering without a full page load.
func (s *Server) logRowsFragment(w http.ResponseWriter, r *http.Request) {
	rows, err := s.logRows(r)
	if err != nil {
		slog.Error("listing events", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.templates["logs"].ExecuteTemplate(w, "logrows", rows); err != nil {
		slog.Error("rendering log rows", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) logRows(r *http.Request) (logRowsData, error) {
	level := r.URL.Query().Get("level")
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	perPage := intParam(r, "per", logsPerPageDefault, 10, 50)
	page := intParam(r, "page", 1, 1, 1_000_000)

	filter := db.EventFilter{Search: search}
	if level == "info" || level == "warn" || level == "error" {
		filter.Levels = []string{level}
	}

	counts, err := s.db.CountEventsByLevel(db.EventFilter{Search: search})
	if err != nil {
		return logRowsData{}, err
	}
	total, err := s.db.CountEvents(filter)
	if err != nil {
		return logRowsData{}, err
	}
	if total > logsWindow {
		total = logsWindow
	}

	pages := (total + perPage - 1) / perPage
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}

	filter.Limit = perPage
	filter.Offset = (page - 1) * perPage
	events, err := s.db.ListEventsFiltered(filter)
	if err != nil {
		return logRowsData{}, err
	}

	servers, err := s.db.ListMediaServers()
	if err != nil {
		return logRowsData{}, err
	}
	downloaders, err := s.db.ListDownloaders()
	if err != nil {
		return logRowsData{}, err
	}

	data := logRowsData{
		Events:   logRows(events, servers, downloaders),
		Level:    level,
		Search:   search,
		PerPage:  perPage,
		Page:     page,
		Pages:    pages,
		Total:    total,
		Window:   logsWindow,
		Counts:   counts,
		CountAll: counts["info"] + counts["warn"] + counts["error"],
		HasPrev:  page > 1,
		HasNext:  page < pages,
		PrevPage: page - 1,
		NextPage: page + 1,
	}
	if total > 0 {
		data.RangeFrom = filter.Offset + 1
		data.RangeTo = filter.Offset + len(data.Events)
	}
	return data, nil
}

func intParam(r *http.Request, name string, fallback, min, max int) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return fallback
	}
	return n
}
