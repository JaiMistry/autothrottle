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

	// throttlingSince is when the current throttle period began, zero
	// while nothing is throttled. historyMu serialises period records.
	throttlingSince time.Time
	historyMu       sync.Mutex

	notify live.Notifier

	// limiterFor is a field so tests can substitute a fake client.
	limiterFor func(downloaderType string) clients.RateLimiter
}

type appliedState struct {
	throttled                  bool
	downloadLimit, uploadLimit int64

	cause   string // the media server that caused it
	failure string // the last apply error, verbatim
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
		// Persisted, so a restart does not silently re-enable throttling.
		paused: store.Paused(),
	}
}

// SetNotifier makes the scheduler announce changes to open pages. Set
// once, before anything runs.
func (s *Scheduler) SetNotifier(n live.Notifier) { s.notify = n }

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
	return DownloaderState{
		Throttled:    state.throttled,
		Cause:        state.cause,
		DownloadKBps: state.downloadLimit,
		UploadKBps:   state.uploadLimit,
		Failure:      state.failure,
		Known:        known,
	}
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
		set[session.Key] = true
		// A sparse event must not erase detail a snapshot supplied.
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

type resolution struct {
	downloader      db.Downloader
	throttled       bool
	causeServerName string
	causeServerID   int64 // for the log's cause tags
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
	// Recorded first so concurrent resolves don't both apply it; a
	// failure below replaces it.
	s.applied[dl.ID] = want
	s.mu.Unlock()

	// After a failure the client's state is unknown, so name it in full.
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
		s.recordFailure(dl.ID, prev.failure, fmt.Sprintf("failed to apply rate limits to %s: %v", dl.Name, clients.Redact(err)))
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
