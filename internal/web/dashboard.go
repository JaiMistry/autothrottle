package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/JaiMistry/autothrottle/internal/auth"
	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/version"
)

// heroState is the Dashboard's one-sentence answer plus the line under
// it. The page leads with a sentence rather than counts because the
// question it exists to answer is "is it working right now, and why is
// it doing what it's doing" — and a count answers neither half.
type heroState struct {
	Statement string
	Reason    string
	Tone      string // warn | good | dim | plain — colours the statement
}

type serverRowView struct {
	Name     string
	Type     string
	State    string
	Sessions int
	Detail   string
}

type downloaderRowView struct {
	Name      string
	Type      string
	State     string
	Cause     string
	Down      int64
	Up        int64
	AppliedAt string
}

type logRowView struct {
	Time    string
	Level   string
	Message string
	Cause   string
}

type dashboardData struct {
	pageData
	Hero            heroState
	ActiveSessions  int
	SessionFootnote string
	ThrottledCount  int
	DownloaderCount int
	ThrottledLabel  string
	Paused          bool
	Configured      bool
	UnreachableNote string
	Servers         []serverRowView
	Downloaders     []downloaderRowView
	Events          []logRowView
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
	events, err := s.db.ListEvents(10)
	if err != nil {
		slog.Error("listing events", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	paused := s.scheduler.Paused()
	data := dashboardData{
		pageData: s.page(r, "Dashboard", "dashboard"),
		Paused:   paused,
		// Throttling needs all three: a server to watch, a downloader to
		// slow down, and a mapping saying the one should affect the
		// other. Missing any of them, the page has exactly one job.
		Configured:      len(mediaServers) > 0 && len(downloaders) > 0 && len(mappings) > 0,
		DownloaderCount: len(downloaders),
		Events:          logRows(events, mediaServers, downloaders),
	}

	var unreachable []string
	totalSessions := 0
	for _, ms := range mediaServers {
		sessions := s.scheduler.ActiveSessionCount(ms.ID)
		totalSessions += sessions
		row := serverRowView{Name: ms.Name, Type: ms.Type, Sessions: sessions}
		switch {
		case !ms.Enabled:
			row.State = "disabled"
			row.Detail = "Not being watched."
		case !s.watchers.Running(ms.ID):
			// Configured and enabled but no live watcher: the connection
			// is down or the type has no implementation.
			row.State = "unreachable"
			row.Detail = "No live connection to this server."
			unreachable = append(unreachable, ms.Name)
		case sessions > 0:
			row.State = "playing"
			row.Detail = detailForSessions(sessions)
		default:
			row.State = "idle"
			row.Detail = "No activity detected."
		}
		data.Servers = append(data.Servers, row)
	}

	throttledNames := []string{}
	var causes []string
	for _, dl := range downloaders {
		state := s.scheduler.State(dl.ID)
		row := downloaderRowView{Name: dl.Name, Type: dl.Type}
		switch {
		case !dl.Enabled:
			row.State = "disabled"
			row.Cause = "Not being managed."
			row.Down, row.Up = dl.DefaultDownloadLimit, dl.DefaultUploadLimit
		case state.Failure != "":
			// A failed apply must never read Normal — the rates on the
			// client are not the rates this app believes it set.
			row.State = "unreachable"
			row.Cause = state.Failure
			unreachable = append(unreachable, dl.Name)
		case !state.Known:
			row.State = "unknown"
			row.Cause = "No state resolved since boot."
			row.Down, row.Up = dl.DefaultDownloadLimit, dl.DefaultUploadLimit
		case state.Throttled:
			row.State = "throttled"
			row.Cause = fmt.Sprintf("because %s is playing", state.Cause)
			row.Down, row.Up = state.DownloadKBps, state.UploadKBps
			throttledNames = append(throttledNames, dl.Name)
			causes = appendUnique(causes, state.Cause)
		default:
			row.State = "normal"
			row.Cause = "Nothing mapped is playing"
			row.Down, row.Up = state.DownloadKBps, state.UploadKBps
		}
		if !state.AppliedAt.IsZero() {
			row.AppliedAt = state.AppliedAt.Local().Format("15:04:05")
		}
		data.Downloaders = append(data.Downloaders, row)
	}

	data.ActiveSessions = totalSessions
	data.ThrottledCount = len(throttledNames)
	data.ThrottledLabel = fmt.Sprintf("%d of %d", len(throttledNames), len(downloaders))
	data.Hero = buildHero(paused, totalSessions, throttledNames, causes, len(downloaders), anyResolved(s, downloaders))
	data.SessionFootnote = sessionFootnote(paused, totalSessions)
	if len(unreachable) > 0 {
		data.UnreachableNote = fmt.Sprintf("%s is currently unreachable. Please check the configuration.",
			joinNames(unreachable))
	}

	s.render(w, "dashboard", data)
}

// buildHero writes the sentence at the top of the page. The wording is
// exact per state — it is the product's headline, not a status string.
func buildHero(paused bool, sessions int, throttled, causes []string, downloaders int, resolved bool) heroState {
	switch {
	case !resolved:
		return heroState{
			Statement: "State not resolved yet",
			Reason:    "No session event has arrived since boot. Rates are whatever the downloader had before AutoThrottle started.",
			Tone:      "dim",
		}
	case paused:
		return heroState{
			Statement: "Default downloader speeds enforced",
			Reason: fmt.Sprintf("%s would normally throttle %s.",
				plural(sessions, "active session", "active sessions"),
				plural(downloaders, "downloader", "downloaders")),
			Tone: "plain",
		}
	case len(throttled) > 0:
		return heroState{
			Statement: fmt.Sprintf("%s throttled", joinNames(throttled)),
			Reason: fmt.Sprintf("because %s is playing · %s",
				joinNames(causes), plural(sessions, "active session", "active sessions")),
			Tone: "warn",
		}
	default:
		return heroState{
			Statement: "Nothing is throttled",
			Reason: fmt.Sprintf("Nothing mapped is playing. %s at their default rate.",
				plural(downloaders, "downloader is", "downloaders are")),
			Tone: "good",
		}
	}
}

func sessionFootnote(paused bool, sessions int) string {
	if paused && sessions > 0 {
		return "counted, but not acted on"
	}
	return "local playback exempt"
}

// anyResolved reports whether the scheduler has decided anything yet.
// Before the first resolve the app genuinely does not know what rates
// are in force, and saying so is better than implying Normal.
func anyResolved(s *Server, downloaders []db.Downloader) bool {
	for _, dl := range downloaders {
		if !dl.Enabled {
			continue
		}
		if st := s.scheduler.State(dl.ID); st.Known {
			return true
		}
	}
	return len(downloaders) == 0
}

// detailForSessions describes what a server is doing. Session titles
// and device names are not recorded yet, so this says only what is
// actually known rather than inventing a "now playing" line.
func detailForSessions(n int) string {
	return plural(n, "session counting toward throttling", "sessions counting toward throttling")
}

func logRows(events []db.Event, servers []db.MediaServer, downloaders []db.Downloader) []logRowView {
	serverName := make(map[int64]string, len(servers))
	for _, ms := range servers {
		serverName[ms.ID] = ms.Name
	}
	downloaderName := make(map[int64]string, len(downloaders))
	for _, dl := range downloaders {
		downloaderName[dl.ID] = dl.Name
	}

	out := make([]logRowView, 0, len(events))
	for _, e := range events {
		row := logRowView{Level: e.Level, Message: e.Message}
		if t, err := time.Parse(db.TimeFormat, e.OccurredAt); err == nil {
			row.Time = t.Local().Format("15:04:05")
		} else {
			row.Time = e.OccurredAt
		}
		// Attribution is structured, not parsed back out of the message:
		// "qBittorrent ← Living Room Plex".
		if e.DownloaderID.Valid && e.MediaServerID.Valid {
			row.Cause = fmt.Sprintf("%s ← %s", downloaderName[e.DownloaderID.Int64], serverName[e.MediaServerID.Int64])
		}
		out = append(out, row)
	}
	return out
}

// joinNames writes a list the way a sentence does: "A", "A and B",
// "A, B and C".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func appendUnique(list []string, v string) []string {
	if v == "" {
		return list
	}
	for _, existing := range list {
		if existing == v {
			return list
		}
	}
	return append(list, v)
}

func (s *Server) togglePause(w http.ResponseWriter, r *http.Request) {
	s.scheduler.SetPaused(r.Context(), !s.scheduler.Paused())
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// page fills the fields every template needs.
func (s *Server) page(r *http.Request, title, active string) pageData {
	return pageData{
		Title:         title,
		Active:        active,
		Authenticated: true,
		CSRFToken:     s.csrf.TokenFor(auth.SessionTokenFromContext(r.Context())),
		Version:       version.Version,
	}
}

func (s *Server) render(w http.ResponseWriter, page string, data any) {
	if err := s.templates[page].ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("rendering page", "page", page, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
