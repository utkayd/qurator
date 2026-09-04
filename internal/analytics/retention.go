package analytics

import (
	"context"
	"log/slog"
	"math/rand"
	"time"

	"github.com/utkayd/qurator/internal/store"
)

// Retention prunes raw scan events older than the configured window (FR-024). It only
// ever calls Store.PruneScanEvents, which by contract never touches rollups, so aggregate
// totals outlive the events that produced them.
type Retention struct {
	store    store.Store
	days     int
	batch    int
	interval time.Duration
	jitter   time.Duration
	now      func() time.Time
	logger   *slog.Logger
}

// NewRetention configures a daily prune keeping `days` of raw events, deleting `batch`
// rows per store call (default 1000) so a year of backlog never takes one long lock.
// days <= 0 disables retention: Run returns immediately and RunOnce deletes nothing.
func NewRetention(st store.Store, days, batch int) *Retention {
	if batch <= 0 {
		batch = 1000
	}
	return &Retention{
		store:    st,
		days:     days,
		batch:    batch,
		interval: 24 * time.Hour,
		jitter:   time.Hour,
		now:      time.Now,
		logger:   slog.Default(),
	}
}

// Run prunes once shortly after start (a small jitter so a fleet restarting together does
// not prune together), then once per day plus jitter, until ctx is cancelled.
func (r *Retention) Run(ctx context.Context) {
	if r.days <= 0 {
		return
	}
	first := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.nextDelay(first)):
		}
		first = false
		n, err := r.RunOnce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			r.logger.Warn("analytics: retention prune failed", "err", err, "deleted", n)
		} else if n > 0 {
			r.logger.Info("analytics: pruned scan events past retention", "deleted", n, "retention_days", r.days)
		}
	}
}

// nextDelay is the interval plus up to `jitter`; the very first run waits for the jitter
// alone so a fresh instance catches up on backlog soon after boot.
func (r *Retention) nextDelay(first bool) time.Duration {
	var j time.Duration
	if r.jitter > 0 {
		j = time.Duration(rand.Int63n(int64(r.jitter)))
	}
	if first {
		return j
	}
	return r.interval + j
}

// RunOnce deletes every raw event older than the retention window, `batch` rows at a
// time until a call removes zero, and reports the total deleted.
func (r *Retention) RunOnce(ctx context.Context) (int64, error) {
	if r.days <= 0 {
		return 0, nil
	}
	before := r.now().UTC().Add(-time.Duration(r.days) * 24 * time.Hour)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := r.store.PruneScanEvents(ctx, before, r.batch)
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, nil
		}
	}
}
