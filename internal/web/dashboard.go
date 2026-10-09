package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/JaiMistry/autothrottle/internal/auth"
	"github.com/JaiMistry/autothrottle/internal/clients"
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
	// Missing names what setup still needs ("a media server"); the
	// reason line then links to Configuration.
	Missing string
	// Since is when the current throttle period began, for the session
	// timer that replaces the reason line while throttling.
	Since time.Time
}

type serverRowView struct {
	Name     string
	Type     string
	State    string
	Sessions int
	// Detail is the one-line message for a server that is not playing
	// anything. Playing servers use Playing instead, one entry per
	// session, so each can carry its own progress.
	Detail  string
	Playing []sessionLineView
}

// sessionLineView is one playing session: what it is, where, and how
// far in. Percent is -1 when the server reported no position, which is
// the signal to draw no progress bar rather than an empty one.
type sessionLineView struct {
	Title  string
	Device string
	Local  bool
	At     string
	Of     string
	// PositionMS and DurationMS let the page keep counting between
	// updates. A player does not ask the server what time it is; it
	// counts, and corrects when the server next speaks. Without this the
	// timecode moves in whatever steps the upstream socket happens to
	// push, which reads as a stutter rather than playback.
	PositionMS int64
	DurationMS int64
	Paused     bool
	Percent    int
}

type downloaderRowView struct {
	Name  string
	Type  string
	State string
	Cause string
	Down  int64
	Up    int64
}

type logRowView struct {
	Time    string
	Level   string
	Message string
	// Attribution is structured rather than parsed back out of the
	// message. Either half may stand alone: an event with only a
	// downloader still names it.
	Downloader string
	Server     string
}

type dashboardData struct {
	// Fragment marks a poll response, which also carries the header
	// pill and pause control as out-of-band swaps.
	Fragment bool
	pageData
	Hero            heroState
	HeroBorder      string // warn | good | "" — the hero card's outline
	Overall         overallFlag
	ActiveSessions  int
	SessionTone     string
	SessionFootnote string
	ThrottledTone   string
	ThrottledCount  int
	Paused          bool
	// Empty is a first run: nothing configured at all. Ready is the
	// minimum for AutoThrottle to act, one media server and one
	// downloader; in between, the page shows what is there.
	Empty       bool
	Ready       bool
	Unreachable []unreachableView
	Servers     []serverRowView
	Downloaders []downloaderRowView
	History     historyView
}

// unreachableView is one row of the backends-unreachable alert.
type unreachableView struct {
	Name string
	Kind string // "Media server" | "Downloader"
}

// overallFlag is the StatusFlag beside the page title.
type overallFlag struct {
	Tone  string
	Label string
	Icon  string
}

// dashboardLive serves the part of the Dashboard that changes on its
// own — throttle state, sessions, playback position — so the page keeps
// up without a refresh.
func (s *Server) dashboardLive(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildDashboardData(r)
	if err != nil {
		slog.Error("building dashboard fragment", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.Fragment = true
	s.renderFragment(w, "dashboard", "dashlive", data)
}

// buildDashboardData assembles the whole page's state, shared by the
// full render and the polled live fragment so the two can never
// disagree about what is happening.
func (s *Server) buildDashboardData(r *http.Request) (dashboardData, error) {
	mediaServers, err := s.db.ListMediaServers()
	if err != nil {
		return dashboardData{}, err
	}
	downloaders, err := s.db.ListDownloaders()
	if err != nil {
		return dashboardData{}, err
	}
	paused := s.scheduler.Paused()
	data := dashboardData{
		// The app's own name: the Dashboard is its home screen.
		pageData: s.page(r, "AutoThrottle", "dashboard"),
		Paused:   paused,
		Empty:    len(mediaServers) == 0 && len(downloaders) == 0,
		Ready:    len(mediaServers) > 0 && len(downloaders) > 0,
	}

	var unreachable []unreachableView
	totalSessions := 0
	countsLocal := false
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
			row.Detail = "Unable to connect"
			unreachable = append(unreachable, unreachableView{ms.Name, "Media server"})
		default:
			conn, known := s.watchers.Connection(ms.ID)
			switch {
			case sessions > 0:
				// Playback wins over a dropped live feed. The
				// reconciliation poll finds sessions over REST whether
				// or not the socket is up, so reporting "connecting"
				// here would hide the thing that actually matters
				// behind a transport detail.
				row.State = "playing"
				row.Playing = sessionLines(s.scheduler.ActiveSessions(ms.ID))
				if len(row.Playing) == 0 {
					// Counted, but nothing described it yet — the event
					// stream carries keys only, and no snapshot has landed.
					row.Detail = s.playingDetail(ms.ID, sessions)
				}
			case known && !conn.Connected:
				row.State = "connecting"
				row.Detail = "No activity detected"
			default:
				row.State = "idle"
				row.Detail = "No activity detected."
			}
		}
		for _, line := range row.Playing {
			if line.Local {
				countsLocal = true
			}
		}
		data.Servers = append(data.Servers, row)
	}

	sortServers(data.Servers)

	throttledNames := []string{}
	unresolvedCount := 0
	allNormal := len(downloaders) > 0
	for _, dl := range downloaders {
		state := s.scheduler.State(dl.ID)
		row := downloaderRowView{Name: dl.Name, Type: dl.Type}
		switch {
		case !dl.Enabled:
			row.State = "disabled"
			row.Cause = "excluded from throttling"
			row.Down, row.Up = dl.DefaultDownloadLimit, dl.DefaultUploadLimit
		case state.Failure != "":
			// A failed apply must never read Normal — the rates on the
			// client are not the rates this app believes it set.
			row.State = "unreachable"
			row.Cause = state.Failure
			unreachable = append(unreachable, unreachableView{dl.Name, "Downloader"})
			unresolvedCount++
			row.Down, row.Up = dl.DefaultDownloadLimit, dl.DefaultUploadLimit
		case !state.Known:
			row.State = "unknown"
			row.Cause = "nothing mapped is playing"
			unresolvedCount++
			row.Down, row.Up = dl.DefaultDownloadLimit, dl.DefaultUploadLimit
		case paused:
			// "Paused" renders as Normal: the configured default rate is
			// in force, which is the same rate as any other unthrottled
			// moment. The reason line carries the override.
			row.State = "paused"
			row.Cause = "throttling disabled"
			row.Down, row.Up = state.DownloadKBps, state.UploadKBps
		case state.Throttled:
			row.State = "throttled"
			if state.Cause != "" {
				row.Cause = fmt.Sprintf("because %s is playing", state.Cause)
			} else {
				row.Cause = "because a mapped server is playing"
			}
			row.Down, row.Up = state.DownloadKBps, state.UploadKBps
			throttledNames = append(throttledNames, dl.Name)
		default:
			row.State = "normal"
			row.Cause = "nothing mapped is playing"
			row.Down, row.Up = state.DownloadKBps, state.UploadKBps
		}
		if row.State != "normal" {
			allNormal = false
		}
		data.Downloaders = append(data.Downloaders, row)
	}

	unresolved := len(downloaders) > 0 && unresolvedCount == len(downloaders)

	data.ActiveSessions = totalSessions
	data.SessionTone = tone(totalSessions > 0, "accent", "dim")
	data.ThrottledTone = tone(len(throttledNames) > 0, "warn", "dim")
	data.ThrottledCount = len(throttledNames)
	data.Hero = buildHero(heroInput{
		paused: paused, sessions: totalSessions, throttled: throttledNames,
		servers: len(mediaServers), downloaders: len(downloaders), unresolved: unresolved,
	})
	if since, ok := s.scheduler.ThrottlingSince(); ok && data.Hero.Tone == "warn" {
		data.Hero.Since = since
	}
	data.Overall = overallStatus(paused, len(throttledNames), data.Empty, data.Ready, unresolved)
	data.SessionFootnote = sessionFootnote(paused, totalSessions, countsLocal)
	data.Unreachable = unreachable
	switch {
	case !data.Ready || paused:
	case len(throttledNames) > 0:
		data.HeroBorder = "warn"
	case allNormal:
		data.HeroBorder = "good"
	}
	return data, nil
}

func (s *Server) dashboardPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildDashboardData(r)
	if err == nil {
		data.History, err = s.loadHistory(historyRange(r))
	}
	if err != nil {
		slog.Error("building dashboard", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.render(w, "dashboard", data)
}

// SinceISO is the throttle period's start, for the timer's datetime.
func (h heroState) SinceISO() string { return h.Since.UTC().Format(time.RFC3339) }

// Elapsed is the timer's first reading: zero-padded hours, minutes and
// seconds, each with its unit.
func (h heroState) Elapsed() [][2]string {
	t := int(max(0, time.Since(h.Since).Seconds()))
	return [][2]string{
		{fmt.Sprintf("%02d", t/3600), "hr"},
		{fmt.Sprintf("%02d", t%3600/60), "m"},
		{fmt.Sprintf("%02d", t%60), "s"},
	}
}

// heroInput is what the headline is decided from.
type heroInput struct {
	paused      bool
	sessions    int
	throttled   []string
	servers     int
	downloaders int
	unresolved  bool
}

// buildHero writes the sentence at the top of the page. The wording is
// exact per state — it is the product's headline, not a status string.
func buildHero(in heroInput) heroState {
	var missing []string
	if in.servers == 0 {
		missing = append(missing, "a media server")
	}
	if in.downloaders == 0 {
		missing = append(missing, "a downloader")
	}
	switch {
	case len(missing) > 0:
		return heroState{
			Statement: "Not ready to throttle",
			Tone:      "dim",
			Missing:   strings.Join(missing, " and "),
		}
	case in.paused:
		h := heroState{Statement: "Default downloader speeds enforced", Tone: "plain"}
		if in.sessions == 0 {
			h.Reason = "Nothing mapped is playing, so nothing would be throttled right now in any case."
		}
		return h
	case len(in.throttled) > 0:
		// Up to two are named; beyond that the rest are counted.
		names := strings.Join(in.throttled, " and ")
		if rest := len(in.throttled) - 2; rest > 0 {
			names = fmt.Sprintf("%s, %s and %s", in.throttled[0], in.throttled[1], count(rest, "other", "others"))
		}
		return heroState{Statement: names + " throttled", Tone: "warn"}
	case in.unresolved:
		return heroState{Statement: "State not resolved yet", Reason: "Nothing mapped is playing.", Tone: "dim"}
	default:
		return heroState{Statement: "Nothing is throttled", Reason: "All downloaders at default rates.", Tone: "good"}
	}
}

// livePosition carries a reported position forward to now. Servers
// report in steps (Plex every ten seconds or so), and a stale figure
// would make the page's ticking clock jump backwards on each refresh.
// Paused sessions, and readings with no timestamp, are not advanced.
func livePosition(session clients.Session, now time.Time) time.Duration {
	if session.Paused || session.PositionAt.IsZero() {
		return session.Position
	}
	elapsed := now.Sub(session.PositionAt)
	if elapsed < 0 {
		return session.Position
	}
	position := session.Position + elapsed
	if session.Duration > 0 && position > session.Duration {
		return session.Duration
	}
	return position
}

// overallStatus is the flag beside the page title.
func overallStatus(paused bool, throttled int, empty, ready, unresolved bool) overallFlag {
	switch {
	case paused:
		return overallFlag{Tone: "good", Label: "Throttling disabled", Icon: "normal"}
	case throttled > 0:
		return overallFlag{Tone: "warn", Label: "Throttling", Icon: "throttled"}
	case empty:
		return overallFlag{Tone: "neutral", Label: "Not configured", Icon: "empty"}
	case !ready:
		return overallFlag{Tone: "neutral", Label: "Setup incomplete", Icon: "empty"}
	case unresolved:
		return overallFlag{Tone: "neutral", Label: "Unknown", Icon: "unknown"}
	default:
		return overallFlag{Tone: "good", Label: "Idle", Icon: "normal"}
	}
}

func tone(test bool, whenTrue, whenFalse string) string {
	if test {
		return whenTrue
	}
	return whenFalse
}

// sessionFootnote qualifies the active-session count, which can include
// local playback when a server does not exempt it.
func sessionFootnote(paused bool, sessions int, countsLocal bool) string {
	if countsLocal && (!paused || sessions == 0) {
		return "includes local playback"
	}
	return ""
}

// timecode renders a position the way a player does: h:mm:ss once past
// an hour, m:ss below it.
func timecode(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	h, m, sec := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%d:%02d", m, sec)
}

// sessionLines turns the scheduler's live sessions into rows. Anything
// the server did not report is left out rather than guessed at.
func sessionLines(sessions []clients.Session) []sessionLineView {
	out := make([]sessionLineView, 0, len(sessions))
	for _, session := range sessions {
		line := sessionLineView{
			Title:   session.Title,
			Device:  session.Device,
			Local:   session.Local,
			Paused:  session.Paused,
			Percent: -1,
		}
		if session.Duration > 0 {
			position := livePosition(session, time.Now())
			line.At = timecode(position)
			line.Of = timecode(session.Duration)
			line.PositionMS = position.Milliseconds()
			line.DurationMS = session.Duration.Milliseconds()
			pct := int(position * 100 / session.Duration)
			line.Percent = min(100, max(0, pct))
		}
		if line.Title == "" && line.Device == "" && line.Percent < 0 {
			continue
		}
		out = append(out, line)
	}
	return out
}

// playingDetail names what is playing and where — "Andor S1E4 — Living
// Room TV · Study iPad". Sessions sharing a title collapse onto one
// line, which is the usual case for a household watching together.
// Anything the server did not report is simply left out.
func (s *Server) playingDetail(mediaServerID int64, sessions int) string {
	records, err := s.db.OpenSessions(mediaServerID)
	if err != nil {
		return count(sessions, "session counting toward throttling", "sessions counting toward throttling")
	}

	byTitle := map[string][]string{}
	var order []string
	for _, rec := range records {
		if _, seen := byTitle[rec.Title]; !seen {
			order = append(order, rec.Title)
		}
		if rec.Device != "" {
			byTitle[rec.Title] = append(byTitle[rec.Title], rec.Device)
		}
	}

	parts := make([]string, 0, len(order))
	for _, title := range order {
		devices := byTitle[title]
		switch {
		case title != "" && len(devices) > 0:
			parts = append(parts, title+" on "+strings.Join(devices, " · "))
		case title != "":
			parts = append(parts, title)
		case len(devices) > 0:
			parts = append(parts, strings.Join(devices, " · "))
		}
	}
	if len(parts) == 0 {
		// Nothing was reported, so say what is known rather than
		// inventing a now-playing line.
		return count(sessions, "session counting toward throttling", "sessions counting toward throttling")
	}
	return strings.Join(parts, " · ")
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
		if e.DownloaderID.Valid {
			row.Downloader = downloaderName[e.DownloaderID.Int64]
		}
		if e.MediaServerID.Valid {
			row.Server = serverName[e.MediaServerID.Int64]
		}
		out = append(out, row)
	}
	return out
}

func count(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func (s *Server) togglePause(w http.ResponseWriter, r *http.Request) {
	s.scheduler.SetPaused(r.Context(), !s.scheduler.Paused())
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// page fills the fields every template needs.
func (s *Server) page(r *http.Request, title, active string) pageData {
	return pageData{
		Asset:         s.assets.URL,
		Title:         title,
		Active:        active,
		Authenticated: true,
		CSRFToken:     s.csrf.TokenFor(auth.SessionTokenFromContext(r.Context())),
		Version:       version.Label(),
	}
}

func (s *Server) render(w http.ResponseWriter, page string, data any) {
	s.renderFragment(w, page, "layout", data)
}

// renderFragment executes one named template from a page's set.
func (s *Server) renderFragment(w http.ResponseWriter, page, name string, data any) {
	if err := s.templates[page].ExecuteTemplate(w, name, data); err != nil {
		slog.Error("rendering", "page", page, "template", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// sortServers puts playing servers first, so the one that matters is
// visible in a list capped at two rows. Stable, and keyed only on
// whether a server plays, so rows move only when playback starts or
// stops — never on a refresh, or when a second stream starts.
func sortServers(rows []serverRowView) {
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].State == "playing" && rows[j].State != "playing"
	})
}
