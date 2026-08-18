// Package scheduler resolves media-server session activity into
// downloader throttle state via the mappings table, and applies it.
// See plan.md's "Throttle logic" section for the full design this
// implements: rates live on the downloader, mappings only decide which
// servers' sessions count toward it, and a downloader is throttled if
// any enabled mapping points at a server with active sessions.
package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/credential"
	"github.com/JaiMistry/autothrottle/internal/crypto"
	"github.com/JaiMistry/autothrottle/internal/db"
)

const applyTimeout = 15 * time.Second

type Scheduler struct {
	db            *db.DB
	encryptionKey []byte

	mu        sync.Mutex
	active    map[int64]map[string]bool // media server ID -> active session keys
	lastState map[int64]bool            // downloader ID -> currently throttled
	paused    bool                      // manual "pause all throttling" override
}

func New(database *db.DB, encryptionKey []byte) *Scheduler {
	return &Scheduler{
		db:            database,
		encryptionKey: encryptionKey,
		active:        make(map[int64]map[string]bool),
		lastState:     make(map[int64]bool),
	}
}

// SetPaused toggles the manual override (plan.md's Dashboard spec) that
// forces every downloader back to its default rate regardless of
// session activity, without discarding the tracked session state —
// unpausing immediately re-resolves against whatever's still playing.
func (s *Scheduler) SetPaused(ctx context.Context, paused bool) {
	s.mu.Lock()
	s.paused = paused
	s.mu.Unlock()
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

// DownloaderThrottled reports a downloader's last-applied throttle
// state for the Dashboard's live view. known is false until the
// scheduler has resolved at least once for this downloader (e.g. right
// after startup, before any session event has fired).
func (s *Scheduler) DownloaderThrottled(downloaderID int64) (throttled, known bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	throttled, known = s.lastState[downloaderID]
	return throttled, known
}

// HandleSessionEvent updates a media server's active-session set and
// re-resolves throttle state for every downloader. Resolution runs in
// its own goroutine (with its own timeout-bound context) so a slow
// downloader API call never blocks the WebSocket read loop that fed
// this event.
func (s *Scheduler) HandleSessionEvent(mediaServerID int64, sessionKey string, active bool) {
	s.mu.Lock()
	set, ok := s.active[mediaServerID]
	if !ok {
		set = make(map[string]bool)
		s.active[mediaServerID] = set
	}
	if active {
		set[sessionKey] = true
	} else {
		delete(set, sessionKey)
	}
	s.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
		defer cancel()
		s.resolve(ctx)
	}()
}

type resolution struct {
	downloader      db.Downloader
	throttled       bool
	causeServerName string
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

	msNameByID := make(map[int64]string, len(mediaServers))
	for _, ms := range mediaServers {
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
		if !paused {
			for _, m := range byDownloader[dl.ID] {
				if len(s.active[m.MediaServerID]) > 0 {
					throttled = true
					causeServerName = msNameByID[m.MediaServerID]
					break
				}
			}
		}
		toApply = append(toApply, resolution{downloader: dl, throttled: throttled, causeServerName: causeServerName})
	}
	s.mu.Unlock()

	for _, r := range toApply {
		s.applyState(ctx, r.downloader, r.throttled, r.causeServerName)
	}
}

func (s *Scheduler) applyState(ctx context.Context, dl db.Downloader, throttled bool, causeServerName string) {
	s.mu.Lock()
	prev, known := s.lastState[dl.ID]
	if known && prev == throttled {
		s.mu.Unlock()
		return
	}
	s.lastState[dl.ID] = throttled
	s.mu.Unlock()

	downloadLimit, uploadLimit := dl.DefaultDownloadLimit, dl.DefaultUploadLimit
	verb := "restored default rate for"
	message := fmt.Sprintf("%s %s (nothing mapped is playing)", verb, dl.Name)
	if throttled {
		downloadLimit, uploadLimit = dl.BackupDownloadLimit, dl.BackupUploadLimit
		verb = "throttled"
		message = fmt.Sprintf("%s %s because %s is playing", verb, dl.Name, causeServerName)
	}

	var username, password string
	if dl.CredentialEncrypted.Valid && dl.CredentialEncrypted.String != "" {
		raw, err := crypto.Decrypt(s.encryptionKey, dl.CredentialEncrypted.String)
		if err != nil {
			s.recordFailure(dl.ID, fmt.Sprintf("decrypting credential for %s: %v", dl.Name, err))
			return
		}
		cred, err := credential.DecodeDownloader(raw)
		if err != nil {
			s.recordFailure(dl.ID, fmt.Sprintf("decoding credential for %s: %v", dl.Name, err))
			return
		}
		username, password = cred.Username, cred.Password
	}

	if err := clients.SetQBittorrentLimits(ctx, dl.URL, username, password, downloadLimit, uploadLimit); err != nil {
		s.recordFailure(dl.ID, fmt.Sprintf("failed to apply rate limits to %s: %v", dl.Name, err))
		return
	}

	if err := s.db.InsertEvent(db.Event{
		Level:        "info",
		Message:      message,
		DownloaderID: sql.NullInt64{Int64: dl.ID, Valid: true},
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
	delete(s.lastState, downloaderID)
	s.mu.Unlock()
	if err := s.db.InsertEvent(db.Event{
		Level:        "error",
		Message:      message,
		DownloaderID: sql.NullInt64{Int64: downloaderID, Valid: true},
	}); err != nil {
		slog.Error("scheduler: recording failure event", "error", err)
	}
}
