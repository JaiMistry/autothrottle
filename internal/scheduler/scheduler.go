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

	// limiterFor resolves a downloader type to its rate-limit client.
	// A field rather than a direct call so tests can substitute a fake
	// and never touch a real download client.
	limiterFor func(downloaderType string) clients.RateLimiter
}

type appliedState struct {
	throttled                  bool
	downloadLimit, uploadLimit int64
	// cause names the media server whose sessions produced this state,
	// and appliedAt is when it was pushed. Both exist for the
	// Dashboard: "Throttled, because Living Room Plex is playing" is
	// the whole product, and it cannot be reconstructed from the
	// throttled flag alone.
	cause     string
	appliedAt time.Time
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
	AppliedAt    time.Time
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
	if prev, known := s.live[mediaServerID][session.Key]; known {
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

// AppliedLimits reports the rate pair last successfully pushed to a
// downloader, and whether that pair is the throttled one. The
// bandwidth sampler records it alongside each sample so analytics can
// show observed throughput against the ceiling in force at the time.
func (s *Scheduler) AppliedLimits(downloaderID int64) (throttled bool, downloadKBps, uploadKBps int64, known bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, known := s.applied[downloaderID]
	return state.throttled, state.downloadLimit, state.uploadLimit, known
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
		AppliedAt:    state.appliedAt,
		Failure:      state.failure,
		Known:        known,
	}
}

// DownloaderThrottled reports a downloader's last-applied throttle
// state for the Dashboard's live view. known is false until the
// scheduler has resolved at least once for this downloader (e.g. right
// after startup, before any session event has fired).
func (s *Scheduler) DownloaderThrottled(downloaderID int64) (throttled, known bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, known := s.applied[downloaderID]
	return state.throttled, known
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

	// Nothing is watching this server any more, so its open sessions
	// can never be observed ending. Left open they would run to the
	// right edge of every future chart.
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

	s.mu.Lock()
	current := s.active[mediaServerID]
	unchanged := sameSessionSet(current, next)
	s.mu.Unlock()

	// The snapshot is the only input that knows *what* is playing — the
	// live event stream carries a key and nothing else — so detail is
	// written on every poll, including the overwhelmingly common one
	// where the session set has not changed at all. Doing this only on
	// a change would leave every session that started via the event
	// stream permanently untitled.
	// Position moves on every poll, so the live view is refreshed even
	// when the session set itself has not changed.
	s.mu.Lock()
	if len(detail) == 0 {
		delete(s.live, mediaServerID)
	} else {
		s.live[mediaServerID] = detail
	}
	s.mu.Unlock()

	for _, session := range sessions {
		if session.Title != "" || session.Device != "" {
			s.recordSession(mediaServerID, session, true)
		}
	}

	if unchanged {
		return
	}

	s.mu.Lock()
	current = s.active[mediaServerID]
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

// Resolve re-evaluates every downloader against current configuration
// and session state, applying any change. Exported so configuration
// changes take effect immediately instead of waiting for the next
// playback event.

// ResolveAsync runs Resolve on its own timeout-bound context, for
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

	s.recordSession(mediaServerID, session, active)
	s.ResolveAsync()
}

// recordSession persists a session's start or end for the Analytics
// page. Failures are logged and swallowed: history is a reporting
// concern, and losing a row must never stop throttling from being
// applied, which is the job that actually matters.
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
	// causeServerID makes the cause queryable. The name alone only
	// exists inside the event's message text, which analytics would
	// have to parse back out of prose to attribute a throttle.
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
	live := make(map[int64]bool, len(toApply))
	for _, r := range toApply {
		live[r.downloader.ID] = true
	}
	for id := range s.applied {
		if !live[id] {
			delete(s.applied, id)
		}
	}
	s.mu.Unlock()

	for _, r := range toApply {
		s.applyState(ctx, r.downloader, r.throttled, r.causeServerName, r.causeServerID)
	}
}

func (s *Scheduler) applyState(ctx context.Context, dl db.Downloader, throttled bool, causeServerName string, causeServerID int64) {
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
	// appliedAt and failure are bookkeeping, not part of "is this the
	// same state" — comparing them would make every pass look like a
	// change and fire an API call per resolve.
	if known && prev.throttled == want.throttled &&
		prev.downloadLimit == want.downloadLimit &&
		prev.uploadLimit == want.uploadLimit &&
		prev.cause == want.cause && prev.failure == "" {
		s.mu.Unlock()
		return
	}
	want.appliedAt = time.Now()
	// Recorded optimistically so concurrent resolves don't both fire
	// the same call; a failure below un-records it.
	s.applied[dl.ID] = want
	s.mu.Unlock()

	if known && prev.throttled == throttled {
		message = fmt.Sprintf("updated rate limits for %s", dl.Name)
	}

	username, password, err := credential.Decrypt(s.encryptionKey, dl.CredentialEncrypted.String)
	if err != nil {
		s.recordFailure(dl.ID, fmt.Sprintf("reading credential for %s: %v", dl.Name, err))
		return
	}

	limiter := s.limiterFor(dl.Type)
	if limiter == nil {
		s.recordFailure(dl.ID, fmt.Sprintf("no rate-limit support for downloader type %q (%s)", dl.Type, dl.Name))
		return
	}

	if err := limiter.SetLimits(ctx, dl.URL, username, password, downloadLimit, uploadLimit); err != nil {
		s.recordFailure(dl.ID, fmt.Sprintf("failed to apply rate limits to %s: %v", dl.Name, err))
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

// recordFailure logs and persists an error, and un-remembers the
// attempted state so the next session event retries rather than
// getting stuck believing a change that never actually applied.
func (s *Scheduler) recordFailure(downloaderID int64, message string) {
	slog.Error("scheduler: applying throttle state", "downloader_id", downloaderID, "error", message)
	s.mu.Lock()
	// The attempted state is un-recorded so the next event retries, but
	// the reason is kept: a downloader that failed to apply must read
	// Unreachable with the machine's own words, never Normal.
	delete(s.applied, downloaderID)
	s.applied[downloaderID] = appliedState{failure: message, appliedAt: time.Now()}
	s.mu.Unlock()
	if err := s.db.InsertEvent(db.Event{
		Level:        "error",
		Message:      message,
		DownloaderID: sql.NullInt64{Int64: downloaderID, Valid: true},
	}); err != nil {
		slog.Error("scheduler: recording failure event", "error", err)
	}
}
