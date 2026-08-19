// Package watcher owns the lifecycle of the per-media-server session
// watchers. Watchers are the app's only live input: one long-lived
// WebSocket connection per enabled media server, feeding session
// start/stop events into the scheduler.
//
// The manager exists so configuration changes take effect immediately
// rather than at next restart. It reconciles the set of running
// watchers against what's currently in the database: starting watchers
// for newly-added servers, stopping them for removed or disabled ones,
// and restarting any whose connection details changed.
package watcher

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/crypto"
	"github.com/JaiMistry/autothrottle/internal/db"
)

// sink receives session events from watchers. The scheduler satisfies
// this; the indirection keeps the manager testable without one.
type sink interface {
	HandleSessionEvent(mediaServerID int64, sessionKey string, active bool)
	ClearSessions(mediaServerID int64)
}

// handle tracks one running watcher. fingerprint captures the config
// the watcher was constructed from — watchers hold their URL, token,
// and exemption flag by value, so a change to any of them means the
// running watcher is stale and has to be replaced rather than updated.
type handle struct {
	cancel      context.CancelFunc
	fingerprint string
}

type Manager struct {
	db            *db.DB
	encryptionKey []byte
	sched         sink

	mu      sync.Mutex
	root    context.Context
	running map[int64]handle
}

func NewManager(database *db.DB, encryptionKey []byte, sched sink) *Manager {
	return &Manager{
		db:            database,
		encryptionKey: encryptionKey,
		sched:         sched,
		running:       make(map[int64]handle),
	}
}

// Start binds the manager to a root context and performs the first
// reconcile. Every watcher started from here on is a child of ctx, so
// cancelling it shuts all of them down.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	m.root = ctx
	m.mu.Unlock()
	m.Reconcile()
}

// Reconcile brings the running watcher set in line with the database.
// Safe to call from an HTTP handler after any media-server change, and
// safe to call repeatedly — servers whose config is unchanged keep
// their existing connection rather than being torn down and redialled.
func (m *Manager) Reconcile() {
	mediaServers, err := m.db.ListMediaServers()
	if err != nil {
		slog.Error("watcher: listing media servers", "error", err)
		return
	}

	desired := make(map[int64]db.MediaServer, len(mediaServers))
	for _, ms := range mediaServers {
		if !ms.Enabled || !supportedType(ms.Type) {
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
		if want && h.fingerprint == m.fingerprint(ms) {
			continue // already running with current config
		}
		h.cancel()
		delete(m.running, id)
		// Sessions counted for this server came from the watcher we
		// just stopped. Leaving them behind would keep a removed or
		// disabled server throttling downloaders forever.
		m.sched.ClearSessions(id)
		slog.Info("watcher: stopped", "media_server_id", id)
	}

	for id, ms := range desired {
		if _, ok := m.running[id]; ok {
			continue
		}
		fingerprint := m.fingerprint(ms)
		if fingerprint == "" {
			continue // credential unreadable; fingerprint logged the reason
		}
		token, err := crypto.Decrypt(m.encryptionKey, ms.CredentialEncrypted)
		if err != nil {
			slog.Error("watcher: decrypting token", "media_server", ms.Name, "error", err)
			continue
		}

		w, err := newWatcher(ms, token)
		if err != nil {
			slog.Error("watcher: unsupported media server", "media_server", ms.Name, "type", ms.Type, "error", err)
			continue
		}

		ctx, cancel := context.WithCancel(m.root)
		go w.Run(ctx)
		go m.forward(ctx, id, w.Events())

		m.running[id] = handle{cancel: cancel, fingerprint: fingerprint}
		slog.Info("watcher: started", "media_server", ms.Name, "url", ms.URL)
	}
}

// newWatcher builds the right SessionWatcher for a media server's
// type. Emby and Jellyfin join here: one case each returning their own
// clients.SessionWatcher implementation, with nothing else in the
// manager, the scheduler, or the web layer needing to change.
func newWatcher(ms db.MediaServer, token string) (clients.SessionWatcher, error) {
	switch ms.Type {
	case "plex":
		return clients.NewPlexWatcher(ms.URL, token, ms.SkipLocalPlayback), nil
	default:
		return nil, fmt.Errorf("no watcher implementation for type %q", ms.Type)
	}
}

// supportedType reports whether a media server type has a watcher
// implementation. Kept next to newWatcher so adding a type is a
// two-line change in one place.
func supportedType(msType string) bool {
	switch msType {
	case "plex":
		return true
	default:
		return false
	}
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
			m.sched.HandleSessionEvent(mediaServerID, event.SessionKey, event.Active)
		}
	}
}

// Running reports which media servers currently have a live watcher —
// for /health and the Dashboard, so "configured" and "actually being
// watched" can be told apart in the UI.
func (m *Manager) Running(mediaServerID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[mediaServerID]
	return ok
}

// fingerprint returns a value that changes whenever anything a running
// watcher captured at construction changes. Decryption failure yields
// "", which callers treat as "can't run this one".
func (m *Manager) fingerprint(ms db.MediaServer) string {
	token, err := crypto.Decrypt(m.encryptionKey, ms.CredentialEncrypted)
	if err != nil {
		slog.Error("watcher: decrypting token for fingerprint", "media_server", ms.Name, "error", err)
		return ""
	}
	// Field separator can't appear in a bool and is unlikely in a URL;
	// a collision would only ever cause a missed restart, not a wrong
	// connection.
	sep := "\x00"
	local := "0"
	if ms.SkipLocalPlayback {
		local = "1"
	}
	return ms.URL + sep + token + sep + local
}
