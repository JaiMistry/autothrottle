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
	Overall         overallFlag
	ActiveSessions  int
	SessionTone     string
	SessionFootnote string
	ThrottledCount  int
	ThrottledTone   string
	ThrottledLabel  string
	Paused          bool
	Empty           bool
	UnreachableNote string
	UnreachableN    int
	Servers         []serverRowView
	Downloaders     []downloaderRowView
	Activity        pagedLog
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
	if err := s.templates["dashboard"].ExecuteTemplate(w, "dashlive", data); err != nil {
		slog.Error("rendering dashboard fragment", "error", err)
	}
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
	activity, err := s.buildActivity(r)
	if err != nil {
		return dashboardData{}, err
	}

	paused := s.scheduler.Paused()
	data := dashboardData{
		pageData: s.page(r, "Dashboard", "dashboard"),
		Paused:   paused,
		// Throttling needs all three: a server to watch, a downloader to
		// slow down, and a mapping saying the one should affect the
		// other. Missing any of them, the page has exactly one job.
		// Nothing to manage means the page has exactly one job: point at
		// Configuration. A downloader is the minimum for anything else
		// on this page to have a subject.
		Empty:    len(downloaders) == 0,
		Activity: activity,
	}

	var unreachable []string
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
			row.Detail = "No live connection to this server."
			unreachable = append(unreachable, ms.Name)
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
				// Nothing to report but the feed is down: say how long
				// it has been down and whether anything is still trying.
				row.State = "connecting"
				row.Detail = reconnectDetail(conn, time.Now())
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

	// Anything playing goes to the top, so the server that matters is
	// the one you can see: with several configured, the list caps at two
	// rows, and the one that just started playing could otherwise be
	// scrolled out of sight.
	//
	// Stable, and keyed only on whether a server is playing — so the
	// configured order survives inside each group and a row moves only
	// when playback actually starts or stops, never on a refresh that
	// changed nothing. Sorting on session *count* instead would reshuffle
	// the top of the list every time a second stream started somewhere.
	sort.SliceStable(data.Servers, func(i, j int) bool {
		return data.Servers[i].State == "playing" && data.Servers[j].State != "playing"
	})

	// Named for the paused reason line, which says what *would* be
	// throttling if the override were off.
	playingServer := ""
	for _, row := range data.Servers {
		if row.State == "playing" {
			playingServer = row.Name
			break
		}
	}

	throttledNames := []string{}
	unresolvedCount := 0
	var causes []string
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
			unreachable = append(unreachable, dl.Name)
			unresolvedCount++
			row.Down, row.Up = dl.DefaultDownloadLimit, dl.DefaultUploadLimit
		case !state.Known:
			row.State = "unknown"
			row.Cause = "no event resolved since boot"
			unresolvedCount++
			row.Down, row.Up = dl.DefaultDownloadLimit, dl.DefaultUploadLimit
		case paused:
			// "Paused" renders as Normal: the configured default rate is
			// in force, which is the same rate as any other unthrottled
			// moment. The reason line carries the override.
			row.State = "paused"
			row.Cause = "at its configured default rate — throttling is off"
			if playingServer != "" {
				row.Cause += fmt.Sprintf(" while %s plays", playingServer)
			}
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
			causes = appendUnique(causes, state.Cause)
		default:
			row.State = "normal"
			row.Cause = "nothing mapped is playing"
			row.Down, row.Up = state.DownloadKBps, state.UploadKBps
		}
		if !state.AppliedAt.IsZero() {
			row.AppliedAt = state.AppliedAt.Local().Format("15:04:05")
		}
		data.Downloaders = append(data.Downloaders, row)
	}

	unresolved := len(downloaders) > 0 && unresolvedCount == len(downloaders)

	data.ActiveSessions = totalSessions
	data.SessionTone = tone(totalSessions > 0, "accent", "dim")
	data.ThrottledCount = len(throttledNames)
	data.ThrottledTone = tone(len(throttledNames) > 0, "warn", "dim")
	data.ThrottledLabel = fmt.Sprintf("%d of %d", len(throttledNames), len(downloaders))
	data.Hero = buildHero(paused, totalSessions, throttledNames, causes, len(downloaders), unresolved)
	data.Overall = overallStatus(paused, len(throttledNames), data.Empty, unresolved)
	data.SessionFootnote = sessionFootnote(paused, totalSessions, countsLocal)
	if len(unreachable) > 0 {
		data.UnreachableN = len(unreachable)
		data.UnreachableNote = fmt.Sprintf("%s is currently unreachable. Please check the configuration.",
			strings.Join(unreachable, " / "))
	}

	return data, nil
}

func (s *Server) dashboardPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildDashboardData(r)
	if err != nil {
		slog.Error("building dashboard", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.render(w, "dashboard", data)
}

// buildHero writes the sentence at the top of the page. The wording is
// exact per state — it is the product's headline, not a status string.
func buildHero(paused bool, sessions int, throttled, causes []string, downloaders int, unresolved bool) heroState {
	switch {
	case paused:
		h := heroState{Statement: "Default downloader speeds enforced", Tone: "plain"}
		if sessions > 0 {
			h.Reason = fmt.Sprintf("%s would normally throttle %s.",
				count(sessions, "active session", "active sessions"),
				count(downloaders, "downloader", "downloaders"))
		} else {
			h.Reason = "Nothing mapped is playing, so nothing would be throttled right now in any case."
		}
		return h
	case len(throttled) > 0:
		verb := "is"
		if len(causes) > 1 {
			verb = "are"
		}
		return heroState{
			Statement: fmt.Sprintf("%s throttled", strings.Join(throttled, " and ")),
			Reason: fmt.Sprintf("because %s %s playing · %s",
				strings.Join(causes, " and "), verb,
				count(sessions, "active session", "active sessions")),
			Tone: "warn",
		}
	case unresolved:
		return heroState{
			Statement: "State not resolved yet",
			Reason:    "No session event has arrived since boot. Rates are whatever the downloader had before AutoThrottle started.",
			Tone:      "dim",
		}
	default:
		tail := fmt.Sprintf("%d downloaders are at their default rate.", downloaders)
		if downloaders == 1 {
			tail = "1 downloader is at its default rate."
		}
		return heroState{
			Statement: "Nothing is throttled",
			Reason:    "Nothing mapped is playing. " + tail,
			Tone:      "good",
		}
	}
}

// livePosition carries a reported position forward to now.
//
// Media servers report position in steps — Plex in roughly ten-second
// ones — so between reports the stored figure is stale by however long
// it has been sitting there. Sending it raw makes every refresh hand
// the browser the same number for several seconds running, and a page
// that ticks a clock forward in between has no choice but to step
// backwards when the stale figure arrives. Advancing it here means each
// refresh returns a figure that has moved, and the two agree.
//
// A paused session is not advanced: its position genuinely is not
// moving. Neither is one whose reading carries no timestamp, which is
// any session the event stream announced before a snapshot described
// it.
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
func overallStatus(paused bool, throttled int, empty, unresolved bool) overallFlag {
	switch {
	case paused:
		return overallFlag{Tone: "good", Label: "Not throttling", Icon: "normal"}
	case throttled > 0:
		return overallFlag{Tone: "warn", Label: "Throttling", Icon: "throttled"}
	case empty:
		return overallFlag{Tone: "neutral", Label: "Not configured", Icon: "empty"}
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

// sessionFootnote qualifies the active-session count.
//
// "local playback exempt" is only true when nothing local is being
// counted. A server with skip_local_playback off will happily count a
// session playing from the LAN, and claiming otherwise under a number
// that includes it would be a plain misstatement.
func sessionFootnote(paused bool, sessions int, countsLocal bool) string {
	switch {
	case paused && sessions > 0:
		return "counted, but not acted on"
	case countsLocal:
		return "includes local playback"
	default:
		return "local playback exempt"
	}
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

// reconnectDetail says how long the feed has been down and when the
// next attempt is due — "WebSocket closed 41s ago — retrying in 30s".
func reconnectDetail(conn clients.ConnectionState, now time.Time) string {
	down := "just now"
	if !conn.Since.IsZero() {
		down = shortDuration(now.Sub(conn.Since)) + " ago"
	}
	if conn.RetryAt.After(now) {
		return fmt.Sprintf("Connection closed %s — retrying in %s", down, shortDuration(conn.RetryAt.Sub(now)))
	}
	return fmt.Sprintf("Connection closed %s — retrying now", down)
}

// shortDuration renders a span the way a status line does: seconds
// under a minute, then minutes, then hours.
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

// playingDetail names what is playing and where — "Andor S1E4 — Living
// Room TV · Study iPad". Sessions sharing a title collapse onto one
// line, which is the usual case for a household watching together.
// Anything the server did not report is simply left out.
func (s *Server) playingDetail(mediaServerID int64, sessions int) string {
	records, err := s.db.ListSessions(0, time.Now().Add(-24*time.Hour), time.Now())
	if err != nil {
		return count(sessions, "session counting toward throttling", "sessions counting toward throttling")
	}

	byTitle := map[string][]string{}
	var order []string
	for _, rec := range records {
		if rec.MediaServerID != mediaServerID || rec.Ended() {
			continue
		}
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
			parts = append(parts, title+" — "+strings.Join(devices, " · "))
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
		Asset:         s.assets.URL,
		Title:         title,
		Active:        active,
		Authenticated: true,
		CSRFToken:     s.csrf.TokenFor(auth.SessionTokenFromContext(r.Context())),
		Version:       version.Label(),
	}
}

func (s *Server) render(w http.ResponseWriter, page string, data any) {
	if err := s.templates[page].ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("rendering page", "page", page, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
