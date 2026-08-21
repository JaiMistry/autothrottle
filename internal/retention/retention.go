// Package retention runs the periodic housekeeping that keeps the
// database from growing without bound: rolling raw bandwidth samples
// up into hourly aggregates, and deleting history events and
// aggregates past their retention window.
//
// Kept apart from the sampler that writes the data, because these run
// on very different cadences — sampling is per-minute, housekeeping is
// hourly — and because a failure in one should never stop the other.
package retention

import (
	"context"
	"log/slog"
	"time"

	"github.com/JaiMistry/autothrottle/internal/db"
	"github.com/JaiMistry/autothrottle/internal/settings"
)

type Runner struct {
	db       *db.DB
	settings *settings.Store
}

func New(database *db.DB, store *settings.Store) *Runner {
	return &Runner{db: database, settings: store}
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
		case <-time.After(r.settings.MaintenanceInterval()):
		}
		r.RunOnce()
	}
}

// RunOnce performs a single maintenance pass. Each step is independent:
// one failing is logged and the rest still run.
func (r *Runner) RunOnce() {
	now := time.Now()

	if folded, err := r.db.RollUpBandwidth(now.Add(-r.settings.BandwidthRawRetention())); err != nil {
		slog.Error("retention: rolling up bandwidth samples", "error", err)
	} else if folded > 0 {
		slog.Info("retention: rolled up bandwidth samples", "samples", folded)
	}

	if deleted, err := r.db.PruneBandwidthRollups(now.Add(-r.settings.BandwidthRollupRetention())); err != nil {
		slog.Error("retention: pruning bandwidth rollups", "error", err)
	} else if deleted > 0 {
		slog.Info("retention: pruned bandwidth rollups", "rows", deleted)
	}

	if deleted, err := r.db.PruneEvents(now.Add(-r.settings.EventRetention())); err != nil {
		slog.Error("retention: pruning events", "error", err)
	} else if deleted > 0 {
		slog.Info("retention: pruned history events", "rows", deleted)
	}

	// Session history shares the event retention window: both answer
	// "what happened, and when", and splitting them would mean a
	// timeline whose sessions and events disagree about how far back
	// the page can look.
	if deleted, err := r.db.PruneSessionHistory(now.Add(-r.settings.EventRetention())); err != nil {
		slog.Error("retention: pruning session history", "error", err)
	} else if deleted > 0 {
		slog.Info("retention: pruned session history", "rows", deleted)
	}
}
