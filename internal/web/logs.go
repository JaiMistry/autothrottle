package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/JaiMistry/autothrottle/internal/auth"
	"github.com/JaiMistry/autothrottle/internal/db"
)

// logsWindow is how far back the page looks. The header says so
// explicitly ("34 of the last 200 events") rather than implying the
// list is everything that ever happened.
const logsWindow = 200

const (
	logsPerPageDefault     = 20
	activityPerPageDefault = 10
)

type logsData struct {
	pageData
	Level    string
	Search   string
	Counts   map[string]int
	CountAll int
	Matching int
	NoEvents bool
	Log      pagedLog
	// LivePanel is the refresh endpoint, held on the panel rather than
	// on the card inside it. The card is not rendered at all when there
	// is nothing to show, so a trigger living there stops the page
	// refreshing exactly when it is emptied — which is precisely when
	// someone is watching for the next event to arrive.
	LivePanel string
}

func (s *Server) logsPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildLogsPage(r)
	if err != nil {
		slog.Error("building logs page", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.render(w, "logs", data)
}

// logPanelFragment serves the tabs and the results together, for a
// change of filter or search term. They have to move as one: the tabs
// carry the selection, and a filter that matches nothing replaces the
// card with an empty state rather than emptying it.
func (s *Server) logPanelFragment(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildLogsPage(r)
	if err != nil {
		slog.Error("building log panel", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderFragment(w, "logs", "logpanelfragment", data)
}

// logRowsFragment serves the card on its own, for paging within a
// filter that is already applied.
func (s *Server) logRowsFragment(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildLogsPage(r)
	if err != nil {
		slog.Error("building log rows", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderFragment(w, "logs", "pagedlog", data.Log)
}

func (s *Server) buildLogsPage(r *http.Request) (logsData, error) {
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
		return logsData{}, err
	}
	total, err := s.db.CountEvents(filter)
	if err != nil {
		return logsData{}, err
	}
	if total > logsWindow {
		total = logsWindow
	}
	// Whether there are any events at all is a different question from
	// whether any match the filter, and the two get different empty
	// states — one points at Configuration, the other at the filter.
	unfiltered, err := s.db.CountEvents(db.EventFilter{})
	if err != nil {
		return logsData{}, err
	}

	rows, page, err := s.pageOfEvents(filter, perPage, page, total)
	if err != nil {
		return logsData{}, err
	}

	extra := url.Values{}
	if level != "" {
		extra.Set("level", level)
	}
	if search != "" {
		extra.Set("q", search)
	}

	data := logsData{
		pageData: s.page(r, "Logs", "logs"),
		Level:    level,
		Search:   search,
		Counts:   counts,
		CountAll: counts["info"] + counts["warn"] + counts["error"],
		Matching: total,
		NoEvents: unfiltered == 0,
		Log:      newPagedLog("Events", "log-card", "/logs/rows", rows, total, perPage, page, extra),
	}
	// The whole panel refreshes, not just the card, so the level counts
	// move with the rows. Not while a search is showing.
	if search == "" {
		q := url.Values{"per": {strconv.Itoa(perPage)}}
		if level != "" {
			q.Set("level", level)
		}
		data.LivePanel = "/logs/panel?" + q.Encode()
	}
	data.Log.ClearEndpoint = "/logs/clear"
	data.Log.ClearTarget = "#log-panel"
	data.Log.CSRFToken = s.csrf.TokenFor(auth.SessionTokenFromContext(r.Context()))
	return data, nil
}

// pageOfEvents reads one page of the filtered log, clamped to the
// window the page advertises.
func (s *Server) pageOfEvents(filter db.EventFilter, perPage, page, total int) ([]logRowView, int, error) {
	page, _ = clampPage(page, total, perPage)
	filter.Limit = perPage
	filter.Offset = (page - 1) * perPage

	events, err := s.db.ListEventsFiltered(filter)
	if err != nil {
		return nil, page, err
	}
	servers, err := s.db.ListMediaServers()
	if err != nil {
		return nil, page, err
	}
	downloaders, err := s.db.ListDownloaders()
	if err != nil {
		return nil, page, err
	}
	return logRows(events, servers, downloaders), page, nil
}

// activityFragment serves the Dashboard's recent-activity card.
func (s *Server) activityFragment(w http.ResponseWriter, r *http.Request) {
	log, err := s.buildActivity(r)
	if err != nil {
		slog.Error("building recent activity", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderFragment(w, "dashboard", "pagedlog", log)
}

func (s *Server) buildActivity(r *http.Request) (pagedLog, error) {
	perPage := intParam(r, "per", activityPerPageDefault, 10, 50)
	page := intParam(r, "page", 1, 1, 1_000_000)

	total, err := s.db.CountEvents(db.EventFilter{})
	if err != nil {
		return pagedLog{}, err
	}
	if total > logsWindow {
		total = logsWindow
	}
	rows, page, err := s.pageOfEvents(db.EventFilter{}, perPage, page, total)
	if err != nil {
		return pagedLog{}, err
	}

	log := newPagedLog("Recent activity", "activity-card", "/activity", rows, total, perPage, page, url.Values{})
	// The Dashboard's copy refreshes itself; only the card moves, so
	// nothing above or below it shifts while you are reading.
	log.LiveEndpoint = fmt.Sprintf("/activity?per=%d", perPage)
	log.LiveTarget = "#activity-card"
	log.EmptyTitle = "No events yet"
	log.EmptyNote = "Throttle state changes and connection failures land here, each with the media server that caused it."
	log.ExtraHref = "/logs"
	log.ExtraLabel = "All logs"
	log.ClearEndpoint = "/activity/clear"
	log.ClearTarget = "#activity-card"
	log.CSRFToken = s.csrf.TokenFor(auth.SessionTokenFromContext(r.Context()))
	return log, nil
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

// clearLogs empties the event log and re-renders the Logs page's card.
// A real delete rather than a filter: the page is cleared so that
// whatever happens next can be watched from a clean slate, which a
// view-only "cleared" flag could not give — it also stopped the live
// refresh, so nothing new appeared at all.
func (s *Server) clearLogs(w http.ResponseWriter, r *http.Request) {
	if !s.clearEvents(w) {
		return
	}
	s.logPanelFragment(w, r)
}

// clearActivity is the same for the Dashboard's copy of the card.
func (s *Server) clearActivity(w http.ResponseWriter, r *http.Request) {
	if !s.clearEvents(w) {
		return
	}
	s.activityFragment(w, r)
}

func (s *Server) clearEvents(w http.ResponseWriter) bool {
	deleted, err := s.db.DeleteAllEvents()
	if err != nil {
		slog.Error("clearing the event log", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	slog.Info("event log cleared", "deleted", deleted)
	return true
}
