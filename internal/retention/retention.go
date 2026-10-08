// Package retention runs the periodic housekeeping that keeps the
// database from growing without bound: events, session and throttle
// history past their retention window, and expired login sessions.
package retention

import (
	"context"
	"log/slog"
	"time"

	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/settings"
)

type Runner struct {
	db *db.DB
}

func New(database *db.DB) *Runner {
	return &Runner{db: database}
}

// Run performs one pass immediately, then repeats on the configured
// maintenance interval until ctx is cancelled.
//
// The pass at startup matters for a container that isn't running
// continuously: a host that's up for twenty minutes a day would
// otherwise never reach a scheduled tick, and retention would silently
// never happen.
func (r *Runner) Run(ctx context.Context) {
	r.RunOnce()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(settings.MaintenanceInterval):
		}
		r.RunOnce()
	}
}

// RunOnce performs a single maintenance pass. Each step is independent:
// one failing is logged and the rest still run.
func (r *Runner) RunOnce() {
	now := time.Now()

	if deleted, err := r.db.PruneEvents(now.Add(-settings.EventRetention)); err != nil {
		slog.Error("retention: pruning events", "error", err)
	} else if deleted > 0 {
		slog.Info("retention: pruned history events", "rows", deleted)
	}

	if deleted, err := r.db.PruneAuthSessions(now); err != nil {
		slog.Error("retention: pruning expired login sessions", "error", err)
	} else if deleted > 0 {
		slog.Info("retention: pruned expired login sessions", "rows", deleted)
	}

	if deleted, err := r.db.PruneSessionHistory(now.Add(-settings.EventRetention)); err != nil {
		slog.Error("retention: pruning session history", "error", err)
	} else if deleted > 0 {
		slog.Info("retention: pruned session history", "rows", deleted)
	}

	if deleted, err := r.db.PruneHistory(now.Add(-settings.EventRetention)); err != nil {
		slog.Error("retention: pruning throttle history", "error", err)
	} else if deleted > 0 {
		slog.Info("retention: pruned throttle history", "rows", deleted)
	}
}
