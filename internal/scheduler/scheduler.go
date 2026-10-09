// Package scheduler resolves media-server session activity into
// downloader throttle state via the mappings table, and applies it.
// The whole design in one sentence: rates live on the downloader,
// mappings only decide which servers' sessions count toward it, and a
// downloader is throttled if any enabled mapping points at a server
// with active sessions.
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
	"github.com/JaiMistry/autothrottle/internal/settings"
)

const applyTimeout = 15 * time.Second

type Scheduler struct {
	db            *db.DB
	encryptionKey []byte
	settings      *settings.Store

	mu     sync.Mutex
	active map[int64]map[string]bool // media server ID -> active session keys
	// live holds the last full description of each active session —
	// what is playing, on what, and how far in. The active set is only
	// keys because that is all throttling needs; the Dashboard needs
	// more, and position changes constantly, so it cannot come from the
	// session history table.
	live map[int64]map[string]clients.Session
	// applied tracks what was last successfully pushed to each
	// downloader — not just whether it's throttled, but the actual
	// rate pair, so editing a limit while a downloader is already in
	// that state still pushes the new numbers.
	applied map[int64]appliedState
	// paused mirrors the persisted throttle.paused setting. Held in
	// memory as well so resolve — which runs on every session event —
	// never has to hit the database for it.
	paused bool

	// throttlingSince is when the current throttle period began: the
	// moment at least one downloader became throttled, zero while none
	// is. historyMu serialises the period records that follow it.
	throttlingSince time.Time
	historyMu       sync.Mutex

	// notify tells open pages when what they show has changed.
	notify live.Notifier

	// limiterFor resolves a downloader type to its rate-limit client.
	// A field rather than a direct call so tests can substitute a fake
	// and never touch a real download client.
	limiterFor func(downloaderType string) clients.RateLimiter
}

type appliedState struct {
	throttled                  bool
	downloadLimit, uploadLimit int64
	// cause names the media server whose sessions produced this
	// state, for the Dashboard's "because Living Room Plex is playing".
	cause string
	// failure holds the last error from applying this downloader's
	// limits, so a row can read Unreachable with the machine's own
	// words rather than a guess.
	failure string
}

// DownloaderState is one downloader's live state, for the Dashboard.
type DownloaderState struct {
	Throttled    bool
	Cause        string
	DownloadKBps int64
	UploadKBps   int64
	Failure      string
	Known        bool
}

func New(database *db.DB, encryptionKey []byte, store *settings.Store) *Scheduler {
	return &Scheduler{
		db:            database,
		encryptionKey: encryptionKey,
		settings:      store,
		active:        make(map[int64]map[string]bool),
		live:          make(map[int64]map[string]clients.Session),
		applied:       make(map[int64]appliedState),
		limiterFor:    clients.RateLimiterFor,
		// Restored from the settings table rather than defaulting to
		// false: someone who paused throttling and then restarted the
		// container should not silently come back to it re-enabled.
		paused: store.Paused(),
	}
}

// SetNotifier makes the scheduler announce changes to open pages. Set
// once, before anything runs.
func (s *Scheduler) SetNotifier(n live.Notifier) { s.notify = n }

// SetPaused toggles the manual dashboard override, which forces every
// downloader back to its default rate regardless of
// session activity, without discarding the tracked session state —
// unpausing immediately re-resolves against whatever's still playing.
func (s *Scheduler) SetPaused(ctx context.Context, paused bool) {
	s.mu.Lock()
	s.paused = paused
	s.mu.Unlock()
	if err := s.settings.SetPaused(paused); err != nil {
		// The in-memory state is already applied, so throttling behaves
		// as asked right now; only persistence across a restart is lost.
		slog.Error("scheduler: persisting pause state", "error", err)
	}
	s.resolve(ctx)
}

func (s *Scheduler) Paused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paused
}

// ActiveSessionCount returns how many sessions are currently counted
// as active for a given media server — for the Dashboard's live view.
func (s *Scheduler) ActiveSessionCount(mediaServerID int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active[mediaServerID])
}

// UpdateSessionDetail refreshes a running session's description — its
// position, its title — without disturbing the active set and without
// re-resolving throttle state. Live sockets send these every second or
// so, and re-resolving on each would mean an API call per tick to every
// mapped downloader for an outcome that cannot have changed.
func (s *Scheduler) UpdateSessionDetail(mediaServerID int64, session clients.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, active := s.active[mediaServerID][session.Key]; !active {
		return
	}
	if s.live[mediaServerID] == nil {
		s.live[mediaServerID] = map[string]clients.Session{}
	}
	// Merge rather than replace. A detail update carries whatever its
	// source could tell us, and that is not always everything: Plex's
	// socket notification has a position and a state but no title,
	// device or duration. Overwriting wholesale would blank the row's
	// identity every time its timecode moved. Emby and Jellyfin send
	// full sessions, so for them this is a no-op.
	prev, known := s.live[mediaServerID][session.Key]
	if known {
		if session.Title == "" {
			session.Title = prev.Title
		}
		if session.Device == "" {
			session.Device = prev.Device
		}
		if session.Duration == 0 {
			session.Duration = prev.Duration
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

// positionDrift is how far a reported position may stray from where
// the page's own clock has it before the page is told: it runs the
// timecode forward itself between reports, so a report that agrees
// with it is not news.
const positionDrift = 2500 * time.Millisecond

// detailChanged reports whether a session's new description differs
// from the old in anything the Dashboard shows. Position counts only
// when it is off from where steady playback would have taken it — a
// seek, a stall — since sockets report it every second or so.
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

// ActiveSessions describes what is currently playing on one media
// server, in a stable order so the Dashboard's rows do not shuffle
// between refreshes. Sessions the event stream started but no snapshot
// has described yet appear with only their key filled in.
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
	return DownloaderState{
		Throttled:    state.throttled,
		Cause:        state.cause,
		DownloadKBps: state.downloadLimit,
		UploadKBps:   state.uploadLimit,
		Failure:      state.failure,
		Known:        known,
	}
}

// ClearSessions drops every tracked session for a media server and
// re-resolves. Called when a server's watcher stops (removed, disabled,
// or reconnecting under new config) so a stopped server can't keep
// downloaders throttled on the strength of sessions nobody is watching
// any more.
func (s *Scheduler) ClearSessions(mediaServerID int64) {
	s.mu.Lock()
	delete(s.active, mediaServerID)
	delete(s.live, mediaServerID)
	s.mu.Unlock()
	live.Notify(s.notify, live.Dashboard)

	// Nothing is watching this server any more, so its open sessions
	// can never be observed ending; left open, they would read as
	// still playing.
	if err := s.db.CloseSessionsForServer(mediaServerID); err != nil {
		slog.Error("scheduler: closing session history", "media_server_id", mediaServerID, "error", err)
	}
	s.ResolveAsync()
}

// ReplaceSessions overwrites a media server's active session set with
// an authoritative snapshot, re-resolving only if it actually differs.
//
// This is the correction path for event-stream drift (see the watcher
// manager's reconcileSessions). The no-change fast path matters: this
// runs on a timer for every server, and the overwhelming majority of
// polls find exactly what's already tracked — re-resolving each time
// would mean a pointless downloader API call per poll per server.
func (s *Scheduler) ReplaceSessions(mediaServerID int64, sessions []clients.Session) {
	next := make(map[string]bool, len(sessions))
	detail := make(map[string]clients.Session, len(sessions))
	for _, session := range sessions {
		next[session.Key] = true
		detail[session.Key] = session
	}

	// Detail is written on every poll, even when the set is unchanged:
	// position moves, and a session that started via the event stream
	// has no title until a snapshot supplies one.
	s.mu.Lock()
	unchanged := sameSessionSet(s.active[mediaServerID], next)
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
	if len(next) == 0 {
		delete(s.active, mediaServerID)
	} else {
		s.active[mediaServerID] = next
	}
	s.mu.Unlock()

	// The snapshot is authoritative, so anything it reveals as started
	// or ended is history the event stream missed.
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
// two sets — what session history records, and what the correction log
// line counts.
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

// ResolveAsync runs resolve on its own timeout-bound context, for
// callers that must not block on downloader API calls — HTTP handlers
// and the WebSocket read loop both use this.
func (s *Scheduler) ResolveAsync() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
		defer cancel()
		s.resolve(ctx)
	}()
}

// HandleSessionEvent updates a media server's active-session set and
// re-resolves throttle state for every downloader. Resolution runs in
// its own goroutine (with its own timeout-bound context) so a slow
// downloader API call never blocks the WebSocket read loop that fed
// this event.
func (s *Scheduler) HandleSessionEvent(mediaServerID int64, session clients.Session, active bool) {
	s.mu.Lock()
	set, ok := s.active[mediaServerID]
	if !ok {
		set = make(map[string]bool)
		s.active[mediaServerID] = set
	}
	if active {
		set[session.Key] = true
		// The event stream carries a key and little else, so a sparse
		// event must not erase detail a snapshot already supplied.
		if session.Title != "" || session.Device != "" || session.Duration > 0 {
			if s.live[mediaServerID] == nil {
				s.live[mediaServerID] = map[string]clients.Session{}
			}
			s.live[mediaServerID][session.Key] = session
		}
	} else {
		delete(set, session.Key)
		delete(s.live[mediaServerID], session.Key)
	}
	s.mu.Unlock()
	// Shown now, before the downloaders answer: the session appears the
	// moment it starts, and the throttle follows when resolve has run.
	live.Notify(s.notify, live.Dashboard)

	s.recordSession(mediaServerID, session, active)
	s.ResolveAsync()
}

// recordSession persists a session's start or end. Failures are logged
// and swallowed: losing a history row must never stop throttling.
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

type resolution struct {
	downloader      db.Downloader
	throttled       bool
	causeServerName string
	// causeServerID attributes the throttle event to its server, for
	// the log's cause tags.
	causeServerID int64
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

	// A disabled media server's watcher is stopped, but defence in
	// depth: its sessions must not count even if a stale entry
	// survives somewhere.
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
	paused := s.paused
	var toApply []resolution
	for _, dl := range downloaders {
		if !dl.Enabled {
			continue
		}
		throttled := false
		causeServerName := ""
		causeServerID := int64(0)
		if !paused {
			for _, m := range byDownloader[dl.ID] {
				if _, enabled := msNameByID[m.MediaServerID]; !enabled {
					continue
				}
				if len(s.active[m.MediaServerID]) > 0 {
					throttled = true
					causeServerName = msNameByID[m.MediaServerID]
					causeServerID = m.MediaServerID
					break
				}
			}
		}
		toApply = append(toApply, resolution{
			downloader:      dl,
			throttled:       throttled,
			causeServerName: causeServerName,
			causeServerID:   causeServerID,
		})
	}

	// Forget downloaders that are gone or switched off, so a later
	// re-enable re-applies its rates from scratch rather than being
	// short-circuited by state remembered from before.
	kept := make(map[int64]bool, len(toApply))
	for _, r := range toApply {
		kept[r.downloader.ID] = true
	}
	for id := range s.applied {
		if !kept[id] {
			delete(s.applied, id)
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
// throttling as a whole starts or stops. Which downloaders are
// throttled can change inside a period without starting a new one.
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

// ThrottlingSince reports when the current throttle period began, for
// the Dashboard's session timer. ok is false while nothing is throttled.
func (s *Scheduler) ThrottlingSince() (since time.Time, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.throttlingSince, !s.throttlingSince.IsZero()
}

func (s *Scheduler) applyState(ctx context.Context, r resolution) {
	dl, throttled, causeServerName, causeServerID := r.downloader, r.throttled, r.causeServerName, r.causeServerID
	downloadLimit, uploadLimit := dl.DefaultDownloadLimit, dl.DefaultUploadLimit
	message := fmt.Sprintf("restored default rate for %s (nothing mapped is playing)", dl.Name)
	if throttled {
		downloadLimit, uploadLimit = dl.BackupDownloadLimit, dl.BackupUploadLimit
		message = fmt.Sprintf("throttled %s because %s is playing", dl.Name, causeServerName)
	}
	want := appliedState{
		throttled:     throttled,
		downloadLimit: downloadLimit,
		uploadLimit:   uploadLimit,
		cause:         causeServerName,
	}

	s.mu.Lock()
	prev, known := s.applied[dl.ID]
	// A recorded failure is never "the same state": it must retry.
	if known && prev.throttled == want.throttled &&
		prev.downloadLimit == want.downloadLimit &&
		prev.uploadLimit == want.uploadLimit &&
		prev.cause == want.cause && prev.failure == "" {
		s.mu.Unlock()
		return
	}
	// Recorded optimistically so concurrent resolves don't both fire
	// the same call; a failure below un-records it.
	s.applied[dl.ID] = want
	s.mu.Unlock()

	// After a failure nothing is known about what the client is running,
	// so the state applied is named in full rather than as an update.
	if known && prev.failure == "" && prev.throttled == throttled {
		message = fmt.Sprintf("updated rate limits for %s", dl.Name)
	}

	username, password, err := credential.Decrypt(s.encryptionKey, dl.CredentialEncrypted.String)
	if err != nil {
		s.recordFailure(dl.ID, prev.failure, fmt.Sprintf("reading credential for %s: %v", dl.Name, err))
		return
	}

	limiter := s.limiterFor(dl.Type)
	if limiter == nil {
		s.recordFailure(dl.ID, prev.failure, fmt.Sprintf("no rate-limit support for downloader type %q (%s)", dl.Type, dl.Name))
		return
	}

	if err := limiter.SetLimits(ctx, dl.URL, username, password, downloadLimit, uploadLimit); err != nil {
		s.recordFailure(dl.ID, prev.failure, fmt.Sprintf("failed to apply rate limits to %s: %v", dl.Name, err))
		return
	}

	if err := s.db.InsertEvent(db.Event{
		Level:         "info",
		Message:       message,
		DownloaderID:  sql.NullInt64{Int64: dl.ID, Valid: true},
		MediaServerID: sql.NullInt64{Int64: causeServerID, Valid: causeServerID != 0},
	}); err != nil {
		slog.Error("scheduler: recording event", "error", err)
	}
}

// recordFailure logs and persists an error. The attempted state is
// replaced by the failure, so the next resolve retries, and the
// Dashboard shows the downloader as unreachable rather than normal.
// recordFailure marks a downloader failing. previous is its failure
// before this attempt, if any: applyState has already overwritten it.
func (s *Scheduler) recordFailure(downloaderID int64, previous, message string) {
	slog.Error("scheduler: applying throttle state", "downloader_id", downloaderID, "error", message)
	s.mu.Lock()
	s.applied[downloaderID] = appliedState{failure: message}
	s.mu.Unlock()
	// Retries hit the same wall until the client is back; the log gets
	// the error once, not once per attempt.
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

// Retry backoff for downloaders whose last apply failed: soon at first,
// for a client that was only restarting, then less often for one that
// is down for longer. Variables so tests need not wait them out.
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

// RetryFailed re-applies throttle state while a downloader is failing,
// until ctx ends. Without it a client that was briefly down — restarting
// for an update, say — stayed Unreachable, with its rates not
// re-asserted, until some unrelated playback event or save happened to
// resolve again.
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

// RetryIfFailing re-applies at once when the downloader's last apply
// failed — for a connection test that has just shown it reachable.
func (s *Scheduler) RetryIfFailing(downloaderID int64) {
	if s.State(downloaderID).Failure != "" {
		s.ResolveAsync()
	}
}
