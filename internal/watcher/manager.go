// Package watcher runs one session watcher per enabled media server and
// keeps that set in line with the database, so configuration changes
// apply without a restart. It also polls each server's sessions to
// correct anything the event stream missed.
package watcher

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"maps"
	"sync"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/crypto"
	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/live"
	"github.com/JaiMistry/autothrottle/internal/settings"
)

// snapshotTimeout bounds one poll, well inside the poll interval, so a
// hung server never stacks overlapping polls.
const snapshotTimeout = 10 * time.Second

// sink receives session events; the scheduler satisfies it.
type sink interface {
	HandleSessionEvent(mediaServerID int64, session clients.Session, active bool)
	ClearSessions(mediaServerID int64)
	ReplaceSessions(mediaServerID int64, sessions []clients.Session)
	UpdateSessionDetail(mediaServerID int64, session clients.Session)
}

// handle tracks one running watcher. fingerprint is the config it was
// built from; any change to it means replacing the watcher.
type handle struct {
	cancel      context.CancelFunc
	fingerprint string
	watcher     clients.SessionWatcher
}

type Manager struct {
	db            *db.DB
	encryptionKey []byte
	sched         sink

	mu      sync.Mutex
	root    context.Context
	running map[int64]handle

	notify live.Notifier
	// reconcileEvery is the snapshot interval; a field so tests can
	// shorten it.
	reconcileEvery time.Duration
}

// SetNotifier makes the manager announce connection changes. Set once,
// before Start.
func (m *Manager) SetNotifier(n live.Notifier) { m.notify = n }

func NewManager(database *db.DB, encryptionKey []byte, sched sink) *Manager {
	return &Manager{
		db:             database,
		encryptionKey:  encryptionKey,
		sched:          sched,
		running:        make(map[int64]handle),
		reconcileEvery: settings.SessionReconcileInterval,
	}
}

// Start does the first reconcile. Every watcher runs under ctx.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	m.root = ctx
	m.mu.Unlock()
	m.Reconcile()
	if m.notify != nil {
		go m.watchHealth(ctx)
	}
}

// healthCheckInterval can be short: health reads memory only.
const healthCheckInterval = 2 * time.Second

// watchHealth announces a watcher starting, stopping, connecting or
// dropping. Polled here rather than threaded out of every client.
func (m *Manager) watchHealth(ctx context.Context) {
	last := m.health()
	t := time.NewTicker(healthCheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if now := m.health(); !maps.Equal(now, last) {
				last = now
				live.Notify(m.notify, live.Dashboard)
			}
		}
	}
}

// health is whether each running watcher is connected.
func (m *Manager) health() map[int64]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[int64]bool, len(m.running))
	for id, h := range m.running {
		out[id] = h.watcher.Connection().Connected
	}
	return out
}

// Reconcile brings the running watchers in line with the database. Safe
// to call repeatedly: unchanged servers keep their connection.
func (m *Manager) Reconcile() {
	mediaServers, err := m.db.ListMediaServers()
	if err != nil {
		slog.Error("watcher: listing media servers", "error", err)
		return
	}

	desired := make(map[int64]db.MediaServer, len(mediaServers))
	for _, ms := range mediaServers {
		if !ms.Enabled || !clients.SupportedMediaServerType(ms.Type) {
			continue // unsupported or switched off — no watcher wanted
		}
		desired[ms.ID] = ms
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.root == nil {
		return // not started yet; Start will do the first reconcile
	}

	for id, h := range m.running {
		ms, want := desired[id]
		if fp, _ := m.fingerprint(ms); want && h.fingerprint == fp {
			continue // already running with current config
		}
		h.cancel()
		delete(m.running, id)
		// Its sessions would otherwise keep downloaders throttled forever.
		m.sched.ClearSessions(id)
		slog.Info("watcher: stopped", "media_server_id", id)
	}

	for id, ms := range desired {
		if _, ok := m.running[id]; ok {
			continue
		}
		fingerprint, token := m.fingerprint(ms)
		if fingerprint == "" {
			continue // credential unreadable; fingerprint logged the reason
		}

		w, err := m.newWatcher(ms, token)
		if err != nil {
			slog.Error("watcher: unsupported media server", "media_server", ms.Name, "type", ms.Type, "error", err)
			continue
		}

		ctx, cancel := context.WithCancel(m.root)
		go w.Run(ctx)
		go m.forward(ctx, id, w.Events())
		go m.reconcileSessions(ctx, id, ms.Name, w)

		m.running[id] = handle{cancel: cancel, fingerprint: fingerprint, watcher: w}
		slog.Info("watcher: started", "media_server", ms.Name, "url", ms.URL)
	}
}

// newWatcher builds the SessionWatcher for a media server's type, with
// its abandoned-pause clocks kept in the database.
func (m *Manager) newWatcher(ms db.MediaServer, token string) (clients.SessionWatcher, error) {
	var w clients.SessionWatcher
	switch ms.Type {
	case "plex":
		w = clients.NewPlexWatcher(ms.URL, token, ms.SkipLocalPlayback)
	case "emby":
		w = clients.NewEmbyWatcher(ms.URL, token, ms.SkipLocalPlayback)
	case "jellyfin":
		w = clients.NewJellyfinWatcher(ms.URL, token, ms.SkipLocalPlayback)
	case "dispatcharr":
		w = clients.NewDispatcharrWatcher(ms.URL, token, ms.SkipLocalPlayback)
	default:
		return nil, fmt.Errorf("no watcher implementation for type %q", ms.Type)
	}
	if pausable, ok := w.(interface{ SetPauseStore(clients.PauseStore) }); ok {
		pausable.SetPauseStore(pauseStore{db: m.db, mediaServerID: ms.ID})
	}
	return w, nil
}

// forward pumps a watcher's events into the scheduler until its
// context is cancelled or the watcher closes its channel.
func (m *Manager) forward(ctx context.Context, mediaServerID int64, events <-chan clients.SessionEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			if event.DetailOnly {
				m.sched.UpdateSessionDetail(mediaServerID, event.Session)
				continue
			}
			m.sched.HandleSessionEvent(mediaServerID, event.Session, event.Active)
		}
	}
}

// reconcileSessions periodically replaces a server's tracked sessions
// with a snapshot. The socket stays the fast path; this corrects drift,
// such as a stop the socket missed while it was down.
func (m *Manager) reconcileSessions(ctx context.Context, mediaServerID int64, name string, w clients.SessionWatcher) {
	failures := 0
	// The first pass runs at once, not after a full interval.
	first := true
	for {
		if !first {
			select {
			case <-ctx.Done():
				return
			case <-time.After(m.reconcileEvery):
			}
		}
		first = false

		if err := m.reconcileOnce(ctx, mediaServerID, w); err != nil {
			if ctx.Err() != nil {
				return
			}
			// The first failure and every tenth after, not every interval.
			if failures == 0 || failures%10 == 0 {
				slog.Warn("watcher: session reconciliation failed", "media_server", name, "consecutive_failures", failures+1, "error", err)
			}
			// Only the first reaches the Logs page.
			if failures == 0 {
				m.note("warn", fmt.Sprintf("Lost contact with %s. Playback there will not be noticed until it answers again.", name), mediaServerID)
			}
			failures++
			continue
		}
		if failures > 0 {
			slog.Info("watcher: session reconciliation recovered", "media_server", name, "after_failures", failures)
			m.note("info", fmt.Sprintf("Back in contact with %s", name), mediaServerID)
			failures = 0
		}
	}
}

// reconcileOnce hands one snapshot to the scheduler.
func (m *Manager) reconcileOnce(ctx context.Context, mediaServerID int64, w clients.SessionWatcher) error {
	snapshotCtx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()

	sessions, err := w.Snapshot(snapshotCtx)
	if err != nil {
		return err
	}
	m.sched.ReplaceSessions(mediaServerID, sessions)
	return nil
}

// note records a line on the Logs page, for the few warnings a user has
// to see: a server that stopped answering cannot report playback.
func (m *Manager) note(level, message string, mediaServerID int64) {
	if err := m.db.InsertEvent(db.Event{
		Level:         level,
		Message:       message,
		MediaServerID: sql.NullInt64{Int64: mediaServerID, Valid: mediaServerID != 0},
	}); err != nil {
		slog.Error("watcher: recording event", "error", err)
	}
}

// SnapshotAll takes a snapshot from every watcher and waits, so startup
// resolves against what is actually playing.
func (m *Manager) SnapshotAll(ctx context.Context) {
	m.mu.Lock()
	ids := make([]int64, 0, len(m.running))
	watchers := make([]clients.SessionWatcher, 0, len(m.running))
	for id, h := range m.running {
		ids = append(ids, id)
		watchers = append(watchers, h.watcher)
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(id int64, w clients.SessionWatcher) {
			defer wg.Done()
			if err := m.reconcileOnce(ctx, id, w); err != nil && ctx.Err() == nil {
				slog.Warn("watcher: first session snapshot failed", "media_server_id", id, "error", err)
			}
		}(ids[i], watchers[i])
	}
	wg.Wait()
}

// Running reports whether a media server has a live watcher.
func (m *Manager) Running(mediaServerID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[mediaServerID]
	return ok
}

// Connection reports a media server's feed health; ok is false when
// nothing is watching it.
func (m *Manager) Connection(mediaServerID int64) (clients.ConnectionState, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, running := m.running[mediaServerID]
	if !running {
		return clients.ConnectionState{}, false
	}
	return h.watcher.Connection(), true
}

// fingerprint is the config a watcher is built from, and the decrypted
// token. An unreadable credential yields "": don't run this one.
func (m *Manager) fingerprint(ms db.MediaServer) (fp, token string) {
	token, err := crypto.Decrypt(m.encryptionKey, ms.CredentialEncrypted)
	if err != nil {
		slog.Error("watcher: decrypting token", "media_server", ms.Name, "error", err)
		return "", ""
	}
	sep := "\x00"
	local := "0"
	if ms.SkipLocalPlayback {
		local = "1"
	}
	return ms.URL + sep + token + sep + local, token
}
