package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/credential"
	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/live"
	"github.com/JaiMistry/autothrottle/internal/schedule"
)

// How often the clock looks even with no window due, and how often each
// downloader is read back. Variables so tests can shorten them.
var (
	clockCheck   = time.Minute
	inspectEvery = time.Minute
)

// RunClock re-resolves when a time window starts or ends, until ctx ends.
// It sleeps until the next edge, waking at least once a minute so a
// changed clock or a missed edge is soon caught.
func (s *Scheduler) RunClock(ctx context.Context) {
	for {
		wait := clockCheck
		if next, ok := s.nextEdge(time.Now()); ok {
			wait = min(wait, time.Until(next))
		}
		// A little after the edge, so the minute has turned.
		t := time.NewTimer(max(wait, 0) + 250*time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if s.windowsMoved(time.Now()) {
			s.resolveNow()
		}
	}
}

// nextEdge is the soonest any enabled downloader's window starts or ends.
func (s *Scheduler) nextEdge(now time.Time) (next time.Time, ok bool) {
	downloaders, windows, err := s.downloadersAndWindows()
	if err != nil {
		return time.Time{}, false
	}
	for _, dl := range downloaders {
		if edge, found := schedule.NextChange(windows[dl.ID], now); found && (!ok || edge.Before(next)) {
			next, ok = edge, true
		}
	}
	return next, ok
}

// windowsMoved reports a downloader whose window in force differs from
// the one last applied.
func (s *Scheduler) windowsMoved(now time.Time) bool {
	downloaders, windows, err := s.downloadersAndWindows()
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exiting {
		return false
	}
	for _, dl := range downloaders {
		applied, known := s.applied[dl.ID]
		if !known || applied.failure != "" {
			continue // resolve and the retry loop own these
		}
		w, in := schedule.Active(windows[dl.ID], now)
		if !sameWindow(applied, appliedState{window: w, inWindow: in}) {
			return true
		}
	}
	return false
}

// downloadersAndWindows lists the enabled downloaders and every window.
func (s *Scheduler) downloadersAndWindows() ([]db.Downloader, map[int64][]schedule.Window, error) {
	all, err := s.db.ListDownloaders()
	if err != nil {
		slog.Error("scheduler: listing downloaders", "error", err)
		return nil, nil, err
	}
	windows, err := s.db.ListWindows()
	if err != nil {
		slog.Error("scheduler: listing time windows", "error", err)
		return nil, nil, err
	}
	enabled := all[:0]
	for _, dl := range all {
		if dl.Enabled {
			enabled = append(enabled, dl)
		}
	}
	return enabled, windows, nil
}

// WatchClients reads every downloader back on a timer, until ctx ends:
// it reports what stops AutoThrottle's limits applying, and puts back
// what someone changed in a downloader AutoThrottle manages.
func (s *Scheduler) WatchClients(ctx context.Context) {
	t := time.NewTicker(inspectEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.inspectAll(ctx)
		}
	}
}

func (s *Scheduler) inspectAll(ctx context.Context) {
	downloaders, windows, err := s.downloadersAndWindows()
	if err != nil {
		return
	}
	seen := make(map[int64]bool, len(downloaders))
	for _, dl := range downloaders {
		seen[dl.ID] = true
		callCtx, cancel := context.WithTimeout(ctx, applyTimeout)
		s.inspectOne(callCtx, dl, len(windows[dl.ID]) > 0 || dl.AlternativeSpeed)
		cancel()
	}
	s.mu.Lock()
	for id := range s.inspected {
		if !seen[id] {
			delete(s.inspected, id)
			delete(s.conflict, id)
		}
	}
	s.mu.Unlock()
}

// inspectOne reads one downloader back. An unreachable one is left to the
// apply path, which already reports it.
func (s *Scheduler) inspectOne(ctx context.Context, dl db.Downloader, managed bool) {
	started := time.Now()
	state, err := s.Inspect(ctx, dl)
	if err != nil {
		slog.Debug("scheduler: reading back a downloader", "downloader_id", dl.ID, "error", clients.Redact(err))
		return
	}
	found := conflictFor(dl, state, managed)

	s.mu.Lock()
	s.inspected[dl.ID] = state
	previous := s.conflict[dl.ID]
	if found == "" {
		delete(s.conflict, dl.ID)
	} else {
		s.conflict[dl.ID] = found
	}
	applied, known := s.applied[dl.ID]
	drifted := managed && known && applied.failure == "" && applied.desired != nil &&
		applied.at.Before(started) && !applied.desired.Matches(state)
	s.mu.Unlock()

	if found != previous {
		live.Notify(s.notify, live.Dashboard)
		if found != "" {
			s.warn(dl, found)
		}
	}
	if drifted {
		s.putBack(ctx, dl, *applied.desired)
	}
}

// conflictFor says what stops AutoThrottle's limits applying in a
// downloader, or "" when nothing does.
func conflictFor(dl db.Downloader, state clients.ClientState, managed bool) string {
	switch {
	case state.Scheduler != "":
		return fmt.Sprintf("%s's own %s is on, so it can override AutoThrottle's limits", dl.Name, state.Scheduler)
	case state.AlternativeOn && !managed:
		// A managed downloader's alternative speed is AutoThrottle's to
		// switch; anyone else's being on means its own limits apply.
		return fmt.Sprintf("%s's alternative speed is on, so its alternative limits apply instead of AutoThrottle's", dl.Name)
	}
	return ""
}

// putBack re-applies what a managed downloader was last given, after
// something outside AutoThrottle changed it.
func (s *Scheduler) putBack(ctx context.Context, dl db.Downloader, want clients.Desired) {
	controller := s.controllerFor(dl.Type)
	username, password, err := credential.Decrypt(s.encryptionKey, dl.CredentialEncrypted.String)
	if controller == nil || err != nil {
		return
	}
	if err := controller.Apply(ctx, dl.URL, username, password, want); err != nil {
		slog.Warn("scheduler: putting a downloader's limits back", "downloader_id", dl.ID, "error", clients.Redact(err))
		return
	}
	s.mu.Lock()
	if current, ok := s.applied[dl.ID]; ok && current.failure == "" {
		current.at = time.Now()
		s.applied[dl.ID] = current
	}
	s.mu.Unlock()
	s.event(dl, 0, fmt.Sprintf("%s's limits were changed outside AutoThrottle, so they were set back", dl.Name))
}

// warn records a warning event about a downloader.
func (s *Scheduler) warn(dl db.Downloader, message string) {
	if err := s.db.InsertEvent(db.Event{
		Level:        "warn",
		Message:      message,
		DownloaderID: sql.NullInt64{Int64: dl.ID, Valid: true},
	}); err != nil {
		slog.Error("scheduler: recording event", "error", err)
	}
}

// Inspect reads a downloader's own limits, alternative speed and
// scheduler, for the check before its first time window is saved.
func (s *Scheduler) Inspect(ctx context.Context, dl db.Downloader) (clients.ClientState, error) {
	controller := s.controllerFor(dl.Type)
	if controller == nil {
		return clients.ClientState{}, fmt.Errorf("no full control for downloader type %q", dl.Type)
	}
	username, password, err := credential.Decrypt(s.encryptionKey, dl.CredentialEncrypted.String)
	if err != nil {
		return clients.ClientState{}, fmt.Errorf("reading credential for %s: %w", dl.Name, err)
	}
	return controller.Inspect(ctx, dl.URL, username, password)
}

// ErrNoConflict is ClearConflicts finding nothing to turn off.
var ErrNoConflict = errors.New("nothing to turn off")

// ClearConflicts turns off what stops AutoThrottle's limits applying in a
// downloader, for the "Turn it off for me" button, then reads it back and
// re-applies AutoThrottle's state.
func (s *Scheduler) ClearConflicts(ctx context.Context, downloaderID int64) error {
	dl, err := s.db.GetDownloader(downloaderID)
	if errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err != nil {
		return fmt.Errorf("loading downloader: %w", err)
	}
	windows, err := s.db.DownloaderWindows(dl.ID)
	if err != nil {
		return fmt.Errorf("loading time windows: %w", err)
	}
	managed := len(windows) > 0 || dl.AlternativeSpeed
	before, err := s.Inspect(ctx, dl)
	if err != nil {
		return clients.Redact(err)
	}
	found := conflictFor(dl, before, managed)
	if found == "" {
		return ErrNoConflict
	}
	controller := s.controllerFor(dl.Type)
	username, password, err := credential.Decrypt(s.encryptionKey, dl.CredentialEncrypted.String)
	if err != nil {
		return fmt.Errorf("reading credential for %s: %w", dl.Name, err)
	}
	if err := controller.ClearConflicts(ctx, dl.URL, username, password); err != nil {
		return clients.Redact(err)
	}
	what := "alternative speed"
	if before.Scheduler != "" {
		what = before.Scheduler
	}
	s.event(dl, 0, fmt.Sprintf("turned off %s's %s from AutoThrottle", dl.Name, what))

	// Forget what was applied, so resolve writes AutoThrottle's state over
	// whatever the scheduler left, then read it back for the page.
	s.mu.Lock()
	delete(s.applied, dl.ID)
	s.mu.Unlock()
	s.resolve(ctx)
	s.inspectOne(ctx, dl, managed)
	return nil
}

// ErrNotReady is a downloader whose own speed scheduler would fight a
// time window or alternative speed: it must be turned off first.
var ErrNotReady = errors.New("the downloader's own scheduler is on")

// Ready checks a downloader can be fully managed before its first time
// window is saved or alternative speed is switched on: reachable, and
// its own speed scheduler off. The error says what's in the way; an
// ErrNotReady one can be cleared with ClearConflicts.
func (s *Scheduler) Ready(ctx context.Context, dl db.Downloader) error {
	if s.controllerFor(dl.Type) == nil {
		return fmt.Errorf("%s can't have time windows", dl.Name)
	}
	state, err := s.Inspect(ctx, dl)
	if err != nil {
		return fmt.Errorf("couldn't check %s: %w", dl.Name, clients.Redact(err))
	}
	// Alternative speed being on is fine: managing it, AutoThrottle
	// switches it.
	if found := conflictFor(dl, state, true); found != "" {
		return fmt.Errorf("%w: %s", ErrNotReady, found)
	}
	return nil
}
