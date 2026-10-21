// Package scheduler turns media-server sessions into downloader throttle
// state and applies it. Rates live on the downloader; a downloader is
// throttled while any enabled mapping points at a server with an active
// session.
package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/credential"
	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/live"
	"github.com/JaiMistry/autothrottle/internal/schedule"
	"github.com/JaiMistry/autothrottle/internal/settings"
)

const applyTimeout = 15 * time.Second

type Scheduler struct {
	db            *db.DB
	encryptionKey []byte
	settings      *settings.Store

	mu     sync.Mutex
	active map[int64]map[string]bool // media server ID -> active session keys
	// live is each active session's latest description (title, device,
	// position) for the Dashboard; throttling needs only the keys.
	live map[int64]map[string]clients.Session
	// applied is what was last pushed to each downloader, rate pair
	// included, so editing a limit in the same state still pushes it.
	applied map[int64]appliedState
	// paused mirrors the persisted setting so resolve never reads it.
	paused bool
	// exiting is set by Release: from then on nothing is throttled.
	exiting bool

	// nextEpisodeBy is, per media server, until when an episode that just
	// ended near its end holds the throttle for autoplay's next one, and
	// releaseAt when each downloader so held goes back to default rates.
	nextEpisodeBy map[int64]time.Time
	releaseAt     map[int64]time.Time
	restoreDelay  time.Duration

	// throttlingSince is when the current throttle period began, zero
	// while nothing is throttled. historyMu serialises period records.
	throttlingSince time.Time
	historyMu       sync.Mutex

	notify live.Notifier

	// inspected is what each downloader last reported about its own
	// limits, and conflict what stops AutoThrottle's limits applying there.
	inspected map[int64]clients.ClientState
	conflict  map[int64]string

	// limiterFor and controllerFor are fields so tests can substitute a
	// fake client.
	limiterFor    func(downloaderType string) clients.RateLimiter
	controllerFor func(downloaderType string) clients.Controller
}

type appliedState struct {
	throttled                  bool
	downloadLimit, uploadLimit int64

	cause   string // the media server that caused it
	failure string // the last apply error, verbatim

	// window is the time window in force, when inWindow. suppressed is
	// playback the window lets through unthrottled.
	window     schedule.Window
	inWindow   bool
	suppressed bool
	// desired is what a managed downloader was put into, nil for one only
	// given its limits; at is when, so a reading taken before it isn't
	// mistaken for someone else's change.
	desired *clients.Desired
	at      time.Time
	// until is when what's in force may next change, and then what
	// follows, for the Dashboard.
	until time.Time
	then  string
}

// DownloaderState is one downloader's live state, for the Dashboard.
type DownloaderState struct {
	Throttled bool
	// Holding: still throttled only for autoplay's next episode, since
	// what was playing ended in its closing stretch.
	Holding      bool
	Cause        string
	DownloadKBps int64
	UploadKBps   int64
	Failure      string
	Known        bool
	// Window is the time window in force, by label; empty for Default.
	// Suppressed: something is playing, but the window says don't
	// throttle. Until is when that may next change, to Then.
	Window     string
	Suppressed bool
	Until      time.Time
	Then       string
	// Conflict is what stops AutoThrottle's limits applying in the
	// client, such as its own scheduler; empty when nothing does.
	Conflict string
}

func New(database *db.DB, encryptionKey []byte, store *settings.Store) *Scheduler {
	return &Scheduler{
		db:            database,
		encryptionKey: encryptionKey,
		settings:      store,
		active:        make(map[int64]map[string]bool),
		live:          make(map[int64]map[string]clients.Session),
		applied:       make(map[int64]appliedState),
		nextEpisodeBy: make(map[int64]time.Time),
		releaseAt:     make(map[int64]time.Time),
		restoreDelay:  settings.RestoreDelay,
		inspected:     make(map[int64]clients.ClientState),
		conflict:      make(map[int64]string),
		limiterFor:    clients.RateLimiterFor,
		controllerFor: clients.ControllerFor,
		// Persisted, so a restart does not silently re-enable throttling.
		paused: store.Paused(),
	}
}

// SetNotifier makes the scheduler announce changes to open pages. Set
// once, before anything runs.
func (s *Scheduler) SetNotifier(n live.Notifier) { s.notify = n }

// SetRestoreDelay changes how long a throttle outlasts the playback that
// caused it; 0 restores at once. Set once, before anything runs: tests
// use it to see a release without waiting.
func (s *Scheduler) SetRestoreDelay(d time.Duration) { s.restoreDelay = d }

// ReleasePending reports whether a downloader is still throttled only
// because an episode ended moments ago, for autoplay's next one.
func (s *Scheduler) ReleasePending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.releaseAt) > 0
}

// SetPaused toggles the override that holds every downloader at its
// default rate. Sessions are still tracked, so resuming re-resolves
// against whatever is playing.
func (s *Scheduler) SetPaused(ctx context.Context, paused bool) {
	s.mu.Lock()
	s.paused = paused
	s.mu.Unlock()
	if err := s.settings.SetPaused(paused); err != nil {
		slog.Error("scheduler: persisting pause state", "error", err)
	}
	s.resolve(ctx)
}

// Release puts every downloader back to its default rates and keeps it
// there, for an exit: a client left throttled stays so until AutoThrottle
// next runs. Nothing is persisted, so the next start throttles afresh.
func (s *Scheduler) Release(ctx context.Context) {
	s.mu.Lock()
	s.exiting = true
	s.mu.Unlock()
	s.resolve(ctx)
}

// PausedSince is when throttling was disabled; ok is false while it is
// not, or if the time was never recorded.
func (s *Scheduler) PausedSince() (time.Time, bool) { return s.settings.PausedSince() }

func (s *Scheduler) Paused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paused
}

// ActiveSessionCount is how many sessions count as active on a server.
func (s *Scheduler) ActiveSessionCount(mediaServerID int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active[mediaServerID])
}

// UpdateSessionDetail refreshes a running session's description without
// touching the active set or re-resolving: sockets send these every
// second or so, and the throttle outcome cannot have changed.
func (s *Scheduler) UpdateSessionDetail(mediaServerID int64, session clients.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, active := s.active[mediaServerID][session.Key]; !active {
		return
	}
	if s.live[mediaServerID] == nil {
		s.live[mediaServerID] = map[string]clients.Session{}
	}
	// Merge, not replace: Plex's socket notification has a position and
	// state but no title, device or duration.
	prev, known := s.live[mediaServerID][session.Key]
	if known {
		// What the item is carries over only while it is the same item: a
		// session can move on to autoplay's next episode, or a film.
		if session.ItemID == "" || session.ItemID == prev.ItemID {
			session.ItemID = prev.ItemID
			if session.Title == "" {
				session.Title = prev.Title
			}
			if session.Duration == 0 {
				session.Duration = prev.Duration
			}
			// Plex's notifications don't say what kind of item it is.
			if !session.Episode {
				session.Episode = prev.Episode
			}
			if !session.Finale {
				session.Finale = prev.Finale
			}
		}
		if session.Device == "" {
			session.Device = prev.Device
		}
		if session.PositionAt.IsZero() {
			session.PositionAt = prev.PositionAt
		}
		if !session.Local {
			session.Local = prev.Local
		}
	}
	s.live[mediaServerID][session.Key] = session
	if !known || detailChanged(prev, session) {
		live.Notify(s.notify, live.Dashboard)
	}
}

// positionDrift is how far a report may stray from the page's own
// running timecode before the page is told.
const positionDrift = 2500 * time.Millisecond

// detailChanged reports a change the Dashboard shows. Position counts
// only when it is off from steady playback: a seek or a stall.
func detailChanged(prev, next clients.Session) bool {
	if prev.Title != next.Title || prev.Device != next.Device || prev.Duration != next.Duration ||
		prev.Paused != next.Paused || prev.Local != next.Local {
		return true
	}
	expected := prev.Position
	if !prev.Paused && !prev.PositionAt.IsZero() && !next.PositionAt.IsZero() {
		expected += next.PositionAt.Sub(prev.PositionAt)
	}
	drift := next.Position - expected
	return drift > positionDrift || drift < -positionDrift
}

// ActiveSessions is what is playing on a server, in a stable order.
// Sessions no snapshot has described yet carry only their key.
func (s *Scheduler) ActiveSessions(mediaServerID int64) []clients.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := s.active[mediaServerID]
	out := make([]clients.Session, 0, len(keys))
	for key := range keys {
		if session, ok := s.live[mediaServerID][key]; ok {
			out = append(out, session)
			continue
		}
		out = append(out, clients.Session{Key: key})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// State returns everything the Dashboard needs about one downloader.
func (s *Scheduler) State(downloaderID int64) DownloaderState {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, known := s.applied[downloaderID]
	_, holding := s.releaseAt[downloaderID]
	out := DownloaderState{
		Throttled:    state.throttled,
		Holding:      holding,
		Cause:        state.cause,
		DownloadKBps: state.downloadLimit,
		UploadKBps:   state.uploadLimit,
		Failure:      state.failure,
		Known:        known,
		Suppressed:   state.suppressed,
		Until:        state.until,
		Then:         state.then,
		Conflict:     s.conflict[downloaderID],
	}
	if state.inWindow {
		out.Window = state.window.Label()
	}
	return out
}

// ClearSessions drops a server's sessions and re-resolves, for when its
// watcher stops: unwatched sessions must not keep anything throttled.
func (s *Scheduler) ClearSessions(mediaServerID int64) {
	s.mu.Lock()
	delete(s.active, mediaServerID)
	delete(s.live, mediaServerID)
	s.mu.Unlock()
	live.Notify(s.notify, live.Dashboard)

	// Nothing will see them end, so close them now.
	if err := s.db.CloseSessionsForServer(mediaServerID); err != nil {
		slog.Error("scheduler: closing session history", "media_server_id", mediaServerID, "error", err)
	}
	s.ResolveAsync()
}

// ReplaceSessions overwrites a server's active set with an authoritative
// snapshot, correcting event-stream drift. It runs on a timer, so it
// re-resolves only when the set actually changed.
func (s *Scheduler) ReplaceSessions(mediaServerID int64, sessions []clients.Session) {
	next := make(map[string]bool, len(sessions))
	detail := make(map[string]clients.Session, len(sessions))
	for _, session := range sessions {
		next[session.Key] = true
		detail[session.Key] = session
	}

	// Detail is written even when the set is unchanged: position moves,
	// and event-started sessions get their title from snapshots.
	s.mu.Lock()
	unchanged := sameSessionSet(s.active[mediaServerID], next)
	// What the ended ones were playing, for noteEnded below.
	previous := s.live[mediaServerID]
	shown := !unchanged || len(s.live[mediaServerID]) != len(detail)
	for key, session := range detail {
		if prev, ok := s.live[mediaServerID][key]; !ok || detailChanged(prev, session) {
			shown = true
		}
	}
	if len(detail) == 0 {
		delete(s.live, mediaServerID)
	} else {
		s.live[mediaServerID] = detail
	}
	s.mu.Unlock()
	if shown {
		live.Notify(s.notify, live.Dashboard)
	}

	for _, session := range sessions {
		if session.Title != "" || session.Device != "" {
			s.recordSession(mediaServerID, session, true)
		}
	}

	if unchanged {
		return
	}

	s.mu.Lock()
	current := s.active[mediaServerID]
	started, ended := changedSessionKeys(current, next)
	now := time.Now()
	if len(started) > 0 {
		s.nextEpisodeArrived(mediaServerID)
	}
	for _, key := range ended {
		s.noteEnded(mediaServerID, previous[key], now)
	}
	if len(next) == 0 {
		delete(s.active, mediaServerID)
	} else {
		s.active[mediaServerID] = next
	}
	s.mu.Unlock()

	// What the snapshot reveals is history the event stream missed.
	for _, key := range started {
		s.recordSession(mediaServerID, detail[key], true)
	}
	for _, key := range ended {
		s.recordSession(mediaServerID, clients.Session{Key: key}, false)
	}
	slog.Info("scheduler: corrected session state from snapshot",
		"media_server_id", mediaServerID, "added", len(started), "removed", len(ended), "now_active", len(next))
	s.ResolveAsync()
}

// changedSessionKeys names the sessions that started and ended between
// two sets.
func changedSessionKeys(current, next map[string]bool) (started, ended []string) {
	for key := range next {
		if !current[key] {
			started = append(started, key)
		}
	}
	for key := range current {
		if !next[key] {
			ended = append(ended, key)
		}
	}
	return started, ended
}

func sameSessionSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for key := range a {
		if !b[key] {
			return false
		}
	}
	return true
}

// ResolveAsync resolves in the background, for callers that must not
// block on downloader API calls.
func (s *Scheduler) ResolveAsync() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
		defer cancel()
		s.resolve(ctx)
	}()
}

// HandleSessionEvent updates a server's active set and re-resolves in
// the background, so a slow downloader never blocks the socket reader.
func (s *Scheduler) HandleSessionEvent(mediaServerID int64, session clients.Session, active bool) {
	s.mu.Lock()
	set, ok := s.active[mediaServerID]
	if !ok {
		set = make(map[string]bool)
		s.active[mediaServerID] = set
	}
	if active {
		if !set[session.Key] {
			s.nextEpisodeArrived(mediaServerID)
		}
		set[session.Key] = true
		// A sparse event must not erase detail a snapshot supplied.
		if session.Title != "" || session.Device != "" || session.Duration > 0 {
			if s.live[mediaServerID] == nil {
				s.live[mediaServerID] = map[string]clients.Session{}
			}
			s.live[mediaServerID][session.Key] = session
		}
	} else {
		if set[session.Key] {
			s.noteEnded(mediaServerID, s.live[mediaServerID][session.Key], time.Now())
		}
		delete(set, session.Key)
		delete(s.live[mediaServerID], session.Key)
	}
	s.mu.Unlock()
	// Shown before the downloaders answer; the throttle follows.
	live.Notify(s.notify, live.Dashboard)

	s.recordSession(mediaServerID, session, active)
	s.ResolveAsync()
}

// recordSession persists a session's start or end. A failure is only
// logged: losing a history row must never stop throttling.
func (s *Scheduler) recordSession(mediaServerID int64, session clients.Session, active bool) {
	var err error
	if active {
		err = s.db.OpenSession(mediaServerID, session.Key, session.Title, session.Device)
	} else {
		err = s.db.CloseSession(mediaServerID, session.Key)
	}
	if err != nil {
		slog.Error("scheduler: recording session history",
			"media_server_id", mediaServerID, "session", session.Key, "active", active, "error", err)
	}
}

// noteEnded gives an episode that ended in its closing stretch a hold on
// its server's downloaders, for autoplay's next episode. Anything else
// restores at once. Called with s.mu held.
func (s *Scheduler) noteEnded(mediaServerID int64, session clients.Session, now time.Time) {
	if s.restoreDelay <= 0 || !nextEpisodeLikely(session, now) {
		return
	}
	if until := now.Add(s.restoreDelay); until.After(s.nextEpisodeBy[mediaServerID]) {
		s.nextEpisodeBy[mediaServerID] = until
	}
}

// nextEpisodeArrived spends a server's hold on a session starting there:
// what the hold waited for has come, and whatever it is now decides when
// the throttle lifts. Called with s.mu held.
func (s *Scheduler) nextEpisodeArrived(mediaServerID int64) {
	delete(s.nextEpisodeBy, mediaServerID)
}

// nextEpisodeLikely: a TV episode with another after it (a finale has
// nothing for autoplay to start), playing (not paused, which an abandoned
// pause ends as) until it stopped within its closing stretch, where the
// credits and autoplay's countdown are. Its last reported position is
// carried forward to now.
func nextEpisodeLikely(session clients.Session, now time.Time) bool {
	if !session.Episode || session.Finale || session.Paused || session.Duration <= 0 {
		return false
	}
	position := session.Position
	if !session.PositionAt.IsZero() {
		position += now.Sub(session.PositionAt)
	}
	return session.Duration-position <= max(settings.EpisodeEnd, session.Duration/10)
}

type resolution struct {
	downloader      db.Downloader
	throttled       bool
	causeServerName string
	causeServerID   int64 // for the log's cause tags

	// managed: the downloader has time windows or uses alternative
	// speed, so AutoThrottle owns all of its limits.
	managed    bool
	window     schedule.Window
	inWindow   bool
	suppressed bool
	until      time.Time
	then       string
}

func (s *Scheduler) resolve(ctx context.Context) {
	mediaServers, err := s.db.ListMediaServers()
	if err != nil {
		slog.Error("scheduler: listing media servers", "error", err)
		return
	}
	downloaders, err := s.db.ListDownloaders()
	if err != nil {
		slog.Error("scheduler: listing downloaders", "error", err)
		return
	}
	mappings, err := s.db.ListMappings()
	if err != nil {
		slog.Error("scheduler: listing mappings", "error", err)
		return
	}
	windows, err := s.db.ListWindows()
	if err != nil {
		slog.Error("scheduler: listing time windows", "error", err)
		return
	}

	// A disabled server's sessions never count, even if some survive.
	msNameByID := make(map[int64]string, len(mediaServers))
	for _, ms := range mediaServers {
		if !ms.Enabled {
			continue
		}
		msNameByID[ms.ID] = ms.Name
	}

	byDownloader := make(map[int64][]db.Mapping)
	for _, m := range mappings {
		if !m.Enabled {
			continue
		}
		byDownloader[m.DownloaderID] = append(byDownloader[m.DownloaderID], m)
	}

	s.mu.Lock()
	paused := s.paused || s.exiting
	now := time.Now()
	var toApply []resolution
	for _, dl := range downloaders {
		if !dl.Enabled {
			continue
		}
		throttled := false
		causeServerName := ""
		causeServerID := int64(0)
		var hold time.Time
		if !paused {
			for _, m := range byDownloader[dl.ID] {
				if _, enabled := msNameByID[m.MediaServerID]; !enabled {
					continue
				}
				if until := s.nextEpisodeBy[m.MediaServerID]; until.After(hold) {
					hold = until
				}
				if len(s.active[m.MediaServerID]) > 0 {
					throttled = true
					causeServerName = msNameByID[m.MediaServerID]
					causeServerID = m.MediaServerID
					break
				}
			}
		}
		switch prev := s.applied[dl.ID]; {
		case throttled, !hold.After(now), !prev.throttled, prev.failure != "":
			// Playing, nothing to wait for, or turned off by the user:
			// paused, or its mapping or server disabled, leave no hold.
			delete(s.releaseAt, dl.ID)
		default:
			// An episode ended near its end, and autoplay's next one may
			// be seconds away: the throttle holds until then.
			if !s.releaseAt[dl.ID].Equal(hold) {
				s.releaseAt[dl.ID] = hold
				time.AfterFunc(hold.Sub(now), s.ResolveAsync)
			}
			throttled, causeServerName = true, prev.cause
		}
		r := resolution{
			downloader:      dl,
			throttled:       throttled,
			causeServerName: causeServerName,
			causeServerID:   causeServerID,
			managed:         len(windows[dl.ID]) > 0 || dl.AlternativeSpeed,
		}
		// Leaving, AutoThrottle restores Default: no one would end a window.
		if !s.exiting {
			r.window, r.inWindow = schedule.Active(windows[dl.ID], now)
			if next, ok := schedule.NextChange(windows[dl.ID], now); ok {
				r.until, r.then = next, "Default"
				if w, ok := schedule.Active(windows[dl.ID], next); ok {
					r.then = w.Label()
				}
			}
		}
		if r.throttled && r.inWindow && !r.window.Throttle {
			r.throttled, r.suppressed = false, true
			delete(s.releaseAt, dl.ID)
		}
		toApply = append(toApply, r)
	}

	// Forget downloaders that are gone or off, so re-enabling one
	// re-applies its rates from scratch.
	kept := make(map[int64]bool, len(toApply))
	for _, r := range toApply {
		kept[r.downloader.ID] = true
	}
	for id := range s.applied {
		if !kept[id] {
			delete(s.applied, id)
		}
	}
	for id := range s.releaseAt {
		if !kept[id] {
			delete(s.releaseAt, id)
		}
	}
	for id, until := range s.nextEpisodeBy {
		if !until.After(now) {
			delete(s.nextEpisodeBy, id)
		}
	}
	s.mu.Unlock()

	for _, r := range toApply {
		s.applyState(ctx, r)
	}
	s.recordThrottlePeriod()
	live.Notify(s.notify, live.Dashboard)
}

// recordThrottlePeriod opens or closes the throttle period when
// throttling as a whole starts or stops.
func (s *Scheduler) recordThrottlePeriod() {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()

	s.mu.Lock()
	throttling := false
	for _, state := range s.applied {
		if state.throttled && state.failure == "" {
			throttling = true
			break
		}
	}
	was := !s.throttlingSince.IsZero()
	now := time.Now()
	switch {
	case throttling && !was:
		s.throttlingSince = now
	case !throttling && was:
		s.throttlingSince = time.Time{}
	}
	s.mu.Unlock()

	var err error
	switch {
	case throttling && !was:
		err = s.db.OpenThrottlePeriod(now)
	case !throttling && was:
		err = s.db.CloseThrottlePeriod(now)
	}
	if err != nil {
		slog.Error("scheduler: recording throttle history", "error", err)
	}
	if throttling != was {
		live.Notify(s.notify, live.History)
	}
}

// ThrottlingSince is when the current throttle period began; ok is
// false while nothing is throttled.
func (s *Scheduler) ThrottlingSince() (since time.Time, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.throttlingSince, !s.throttlingSince.IsZero()
}

func (s *Scheduler) applyState(ctx context.Context, r resolution) {
	dl, throttled, causeServerName, causeServerID := r.downloader, r.throttled, r.causeServerName, r.causeServerID

	// What's in force without playback: Default, or the window's rates.
	base := clients.Limits{DownloadKBps: dl.DefaultDownloadLimit, UploadKBps: dl.DefaultUploadLimit}
	if r.inWindow {
		base = clients.Limits{DownloadKBps: r.window.DownloadKBps, UploadKBps: r.window.UploadKBps}
	}
	// Throttling never raises a limit, so a window slower than the
	// Throttled rates keeps its own.
	throttledLimits := clients.Limits{
		DownloadKBps: lowerLimit(dl.BackupDownloadLimit, base.DownloadKBps),
		UploadKBps:   lowerLimit(dl.BackupUploadLimit, base.UploadKBps),
	}
	limits := base
	if throttled {
		limits = throttledLimits
	}

	// A managed downloader is put wholly into AutoThrottle's state:
	// alternative speed off, or carrying the throttle where it's wanted.
	var desired *clients.Desired
	if r.managed {
		d := clients.Desired{Normal: limits}
		if clients.HasAlternativeSpeed(dl.Type) {
			on := throttled && dl.AlternativeSpeed
			if dl.AlternativeSpeed {
				alternative := throttledLimits
				d.Normal, d.Alternative = base, &alternative
			}
			d.AlternativeOn = &on
		}
		desired = &d
	}

	want := appliedState{
		throttled:     throttled,
		downloadLimit: limits.DownloadKBps,
		uploadLimit:   limits.UploadKBps,
		cause:         causeServerName,
		window:        r.window,
		inWindow:      r.inWindow,
		suppressed:    r.suppressed,
		desired:       desired,
		until:         r.until,
		then:          r.then,
	}

	s.mu.Lock()
	prev, known := s.applied[dl.ID]
	exiting := s.exiting
	// A recorded failure is never "the same state": it must retry.
	sameClient := known && prev.failure == "" && prev.downloadLimit == want.downloadLimit &&
		prev.uploadLimit == want.uploadLimit && sameDesired(prev.desired, want.desired)
	if sameClient && prev.throttled == want.throttled && prev.cause == want.cause &&
		sameWindow(prev, want) && prev.suppressed == want.suppressed {
		prev.until, prev.then = want.until, want.then
		s.applied[dl.ID] = prev
		s.mu.Unlock()
		return
	}
	// Nothing to send, only what it means: the Dashboard's story changes.
	if sameClient && prev.throttled == want.throttled {
		want.at = prev.at
		s.applied[dl.ID] = want
		s.mu.Unlock()
		if want.suppressed && !prev.suppressed {
			s.event(dl, causeServerID, fmt.Sprintf("not throttling %s while %s plays: its %s window allows it", dl.Name, causeServerName, r.window.Label()))
		}
		return
	}
	// Recorded first so concurrent resolves don't both apply it; a
	// failure below replaces it.
	s.applied[dl.ID] = want
	s.mu.Unlock()

	message := applyMessage(dl, r, prev, known, exiting)

	username, password, err := credential.Decrypt(s.encryptionKey, dl.CredentialEncrypted.String)
	if err != nil {
		s.recordFailure(dl.ID, prev.failure, fmt.Sprintf("reading credential for %s: %v", dl.Name, err))
		return
	}

	if desired != nil {
		controller := s.controllerFor(dl.Type)
		if controller == nil {
			s.recordFailure(dl.ID, prev.failure, fmt.Sprintf("no full control for downloader type %q (%s)", dl.Type, dl.Name))
			return
		}
		err = controller.Apply(ctx, dl.URL, username, password, *desired)
	} else {
		limiter := s.limiterFor(dl.Type)
		if limiter == nil {
			s.recordFailure(dl.ID, prev.failure, fmt.Sprintf("no rate-limit support for downloader type %q (%s)", dl.Type, dl.Name))
			return
		}
		err = limiter.SetLimits(ctx, dl.URL, username, password, limits.DownloadKBps, limits.UploadKBps)
	}
	if err != nil {
		s.recordFailure(dl.ID, prev.failure, fmt.Sprintf("failed to apply rate limits to %s: %v", dl.Name, clients.Redact(err)))
		return
	}
	s.mu.Lock()
	if current := s.applied[dl.ID]; current.failure == "" {
		current.at = time.Now()
		s.applied[dl.ID] = current
	}
	s.mu.Unlock()

	s.event(dl, causeServerID, message)
}

// applyMessage says why a downloader's rates are changing.
func applyMessage(dl db.Downloader, r resolution, prev appliedState, known, exiting bool) string {
	// After a failure the client's state is unknown, so name it in full.
	sameStory := known && prev.failure == "" && prev.throttled == r.throttled &&
		sameWindow(prev, appliedState{window: r.window, inWindow: r.inWindow})
	switch {
	case sameStory:
		return fmt.Sprintf("updated rate limits for %s", dl.Name)
	case r.throttled:
		return fmt.Sprintf("throttled %s because %s is playing", dl.Name, r.causeServerName)
	case exiting:
		return fmt.Sprintf("restored default rate for %s (AutoThrottle is exiting)", dl.Name)
	case r.inWindow && known && prev.throttled:
		return fmt.Sprintf("restored %s's %s window rate (nothing mapped is playing)", dl.Name, r.window.Label())
	case r.inWindow:
		return fmt.Sprintf("applied %s's %s window rate", dl.Name, r.window.Label())
	case known && prev.inWindow && !prev.throttled:
		return fmt.Sprintf("restored default rate for %s (its %s window ended)", dl.Name, prev.window.Label())
	}
	return fmt.Sprintf("restored default rate for %s (nothing mapped is playing)", dl.Name)
}

// event records an info event about a downloader.
func (s *Scheduler) event(dl db.Downloader, causeServerID int64, message string) {
	if err := s.db.InsertEvent(db.Event{
		Level:         "info",
		Message:       message,
		DownloaderID:  sql.NullInt64{Int64: dl.ID, Valid: true},
		MediaServerID: sql.NullInt64{Int64: causeServerID, Valid: causeServerID != 0},
	}); err != nil {
		slog.Error("scheduler: recording event", "error", err)
	}
}

// lowerLimit is the stricter of two limits, 0 being unlimited.
func lowerLimit(a, b int64) int64 {
	switch {
	case a <= 0:
		return max(b, 0)
	case b <= 0:
		return a
	}
	return min(a, b)
}

func sameWindow(a, b appliedState) bool {
	return a.inWindow == b.inWindow && (!a.inWindow || a.window == b.window)
}

func sameDesired(a, b *clients.Desired) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Normal == b.Normal &&
		(a.Alternative == nil) == (b.Alternative == nil) && (a.Alternative == nil || *a.Alternative == *b.Alternative) &&
		(a.AlternativeOn == nil) == (b.AlternativeOn == nil) && (a.AlternativeOn == nil || *a.AlternativeOn == *b.AlternativeOn)
}

// recordFailure marks a downloader failing, so the next resolve retries
// and it reads Unreachable, never Normal. previous is its failure before
// this attempt: applyState has already overwritten it.
func (s *Scheduler) recordFailure(downloaderID int64, previous, message string) {
	slog.Error("scheduler: applying throttle state", "downloader_id", downloaderID, "error", message)
	s.mu.Lock()
	s.applied[downloaderID] = appliedState{failure: message}
	s.mu.Unlock()
	// Logged once per outage, not once per retry.
	if previous == message {
		return
	}
	if err := s.db.InsertEvent(db.Event{
		Level:        "error",
		Message:      message,
		DownloaderID: sql.NullInt64{Int64: downloaderID, Valid: true},
	}); err != nil {
		slog.Error("scheduler: recording failure event", "error", err)
	}
}

// Retry backoff after a failed apply: soon, for a client that was only
// restarting, then less often. Variables so tests can shorten them.
var (
	retryFirst = 15 * time.Second
	retryMax   = 2 * time.Minute
	retryCheck = 5 * time.Second
)

// failing reports whether any downloader's last apply failed.
func (s *Scheduler) failing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, state := range s.applied {
		if state.failure != "" {
			return true
		}
	}
	return false
}

// RetryFailed re-applies throttle state while any downloader is failing,
// until ctx ends, so a client back from a restart gets its rates.
func (s *Scheduler) RetryFailed(ctx context.Context) {
	t := time.NewTicker(retryCheck)
	defer t.Stop()
	delay := retryFirst
	var next time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if !s.failing() {
				delay, next = retryFirst, time.Time{}
				continue
			}
			if next.IsZero() {
				next = now.Add(delay)
				continue
			}
			if now.Before(next) {
				continue
			}
			s.resolveNow()
			delay = min(delay*2, retryMax)
			next = now.Add(delay)
		}
	}
}

// resolveNow is resolve on its own timeout-bound context, waited for.
func (s *Scheduler) resolveNow() {
	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()
	s.resolve(ctx)
}

// RetryIfFailing re-applies at once if the downloader is failing, for a
// connection test that has just shown it reachable.
func (s *Scheduler) RetryIfFailing(downloaderID int64) {
	if s.State(downloaderID).Failure != "" {
		s.ResolveAsync()
	}
}
