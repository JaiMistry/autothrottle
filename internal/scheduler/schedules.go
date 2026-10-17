package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/credential"
	"github.com/JaiMistry/autothrottle/internal/db"
)

func scheduleStateKey(id int64) string { return fmt.Sprintf("qbittorrent_schedule_%d", id) }

// RefreshSchedules follows client-side mode/rate edits while the session
// set stays unchanged. Both rate banks are already capped, so a clock
// boundary does not wait for this poll before taking effect.
func (s *Scheduler) RefreshSchedules(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.mu.Lock()
			managed := false
			for _, state := range s.applied {
				managed = managed || state.scheduled
			}
			s.mu.Unlock()
			if managed {
				callCtx, cancel := context.WithTimeout(ctx, applyTimeout)
				s.resolveWithSchedules(callCtx, true)
				cancel()
			}
		}
	}
}

// restoreScheduled runs under resolveMu. Empty recovery state is a no-op,
// so it never applies a fixed default to a scheduled or disabled client.
func (s *Scheduler) restoreScheduled(ctx context.Context, dl db.Downloader) error {
	value, _, err := s.db.GetSetting(scheduleStateKey(dl.ID))
	if err != nil || value == "" {
		return err
	}
	username, password, err := credential.Decrypt(s.encryptionKey, dl.CredentialEncrypted.String)
	if err != nil {
		return err
	}
	_, err = (clients.QBittorrent{}).ApplyScheduledLimits(ctx, dl.URL, username, password, false, 0, 0, s.db, scheduleStateKey(dl.ID))
	return err
}

// Shutdown restores outstanding scheduled-rate overlays before the process
// exits and prevents a queued session event from reapplying one afterwards.
// A failed restore retains its durable recovery state for the next start.
func (s *Scheduler) Shutdown(ctx context.Context) error {
	s.resolveMu.Lock()
	defer s.resolveMu.Unlock()
	s.stopped = true
	downloaders, err := s.db.ListDownloaders()
	if err != nil {
		return err
	}
	var failures []error
	for _, dl := range downloaders {
		if err := s.restoreScheduled(ctx, dl); err != nil {
			failures = append(failures, fmt.Errorf("restoring %s: %w", dl.Name, err))
		}
	}
	return errors.Join(failures...)
}

// UpdateDownloader restores a client's owned rate changes before changing
// its address, credentials, type, or enabled state. Recovery and the edit
// are serialised with playback events, so stale work cannot throttle the
// old endpoint again after the configuration changes.
func (s *Scheduler) UpdateDownloader(ctx context.Context, next db.Downloader) error {
	s.resolveMu.Lock()
	defer s.resolveMu.Unlock()
	old, err := s.db.GetDownloader(next.ID)
	if err != nil {
		return err
	}
	if old.URL != next.URL || old.Type != next.Type || old.CredentialEncrypted != next.CredentialEncrypted || !next.Enabled {
		if err := s.restoreScheduled(ctx, old); err != nil {
			return err
		}
		s.mu.Lock()
		delete(s.applied, next.ID)
		s.mu.Unlock()
	}
	return s.db.UpdateDownloader(next)
}

func (s *Scheduler) DeleteDownloader(ctx context.Context, id int64) error {
	s.resolveMu.Lock()
	defer s.resolveMu.Unlock()
	dl, err := s.db.GetDownloader(id)
	if err != nil {
		return err
	}
	if err := s.restoreScheduled(ctx, dl); err != nil {
		return err
	}
	return s.db.DeleteDownloader(id)
}
