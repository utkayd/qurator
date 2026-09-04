package analytics

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/store"
)

// Counter is the drop counter the Recorder increments. prometheus.Counter satisfies it;
// tests use a plain atomic.
type Counter interface {
	Inc()
	Add(float64)
}

// Flusher is the shutdown shape cmd/qurator calls: drain what can be drained within the
// context's deadline, then return.
type Flusher interface {
	Close(ctx context.Context) error
}

// Options tunes the pipeline. Zero values take the config defaults
// (analytics.buffer_size 10000, batch_size 200, flush_interval 500ms) and two workers
// (research.md §4: 2–4 consumers).
type Options struct {
	BufferSize    int
	BatchSize     int
	FlushInterval time.Duration
	Workers       int
	// FlushTimeout bounds one InsertScanBatch call so a slow store cannot pin a worker
	// forever. Default 10s.
	FlushTimeout time.Duration
	// Logger receives warnings about failed batches. Default slog.Default().
	Logger *slog.Logger
}

func (o Options) withDefaults() Options {
	if o.BufferSize <= 0 {
		o.BufferSize = 10_000
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 200
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = 500 * time.Millisecond
	}
	if o.Workers <= 0 {
		o.Workers = 2
	}
	if o.FlushTimeout <= 0 {
		o.FlushTimeout = 10 * time.Second
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return o
}

// Recorder is the asynchronous scan-event pipeline: a bounded channel fed by Record and
// drained by consumer goroutines that batch events into the store together with their
// rollup deltas. It implements domain.Recorder and Flusher.
type Recorder struct {
	store   store.Store
	opts    Options
	dropped Counter
	ch      chan domain.ScanEvent

	mu     sync.RWMutex // guards closed and the channel close; Record holds it for nanoseconds
	closed bool
	wg     sync.WaitGroup

	inflight atomic.Int64 // events taken off the channel and not yet persisted or dropped
}

// NewRecorder starts the consumer goroutines and returns a ready Recorder. dropped is
// incremented once per event that could not be recorded — buffer full, recorder closed,
// or the store rejecting a batch.
func NewRecorder(st store.Store, opts Options, dropped Counter) *Recorder {
	opts = opts.withDefaults()
	r := &Recorder{
		store:   st,
		opts:    opts,
		dropped: dropped,
		ch:      make(chan domain.ScanEvent, opts.BufferSize),
	}
	r.wg.Add(opts.Workers)
	for i := 0; i < opts.Workers; i++ {
		go r.worker()
	}
	return r
}

// Record enqueues ev or drops it. It never blocks and never allocates: the event is
// passed by value into a pre-sized channel, and the only branch on the slow path is the
// drop counter. A closed Recorder drops too, rather than panicking on a closed channel.
func (r *Recorder) Record(ev domain.ScanEvent) {
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		r.dropped.Inc()
		return
	}
	select {
	case r.ch <- ev:
	default:
		r.dropped.Inc()
	}
	r.mu.RUnlock()
}

// Close stops accepting events, closes the channel so workers drain what remains, and
// waits for them until ctx expires. It returns ctx.Err() if the deadline hit first; the
// number of events that were not persisted is then available from Unflushed. Calling
// Close more than once is safe.
func (r *Recorder) Close(ctx context.Context) error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.ch)
	}
	r.mu.Unlock()

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Unflushed reports how many accepted events have not yet been persisted: those still in
// the buffer plus those held by a worker mid-flush. After a clean Close it is zero.
func (r *Recorder) Unflushed() int64 {
	return int64(len(r.ch)) + r.inflight.Load()
}

func (r *Recorder) worker() {
	defer r.wg.Done()
	ticker := time.NewTicker(r.opts.FlushInterval)
	defer ticker.Stop()

	batch := make([]domain.ScanEvent, 0, r.opts.BatchSize)
	for {
		select {
		case ev, ok := <-r.ch:
			if !ok {
				r.flush(batch)
				return
			}
			r.inflight.Add(1)
			batch = append(batch, ev)
			if len(batch) >= r.opts.BatchSize {
				r.flush(batch)
				batch = make([]domain.ScanEvent, 0, r.opts.BatchSize)
			}
		case <-ticker.C:
			if len(batch) > 0 {
				r.flush(batch)
				batch = make([]domain.ScanEvent, 0, r.opts.BatchSize)
			}
		}
	}
}

// flush writes one batch with its rollups in a single store transaction. A store error
// is logged and the whole batch is counted as dropped; the worker carries on, because a
// broken analytics sink must never take the redirect path down with it.
func (r *Recorder) flush(batch []domain.ScanEvent) {
	n := len(batch)
	if n == 0 {
		return
	}
	defer r.inflight.Add(-int64(n))

	ctx, cancel := context.WithTimeout(context.Background(), r.opts.FlushTimeout)
	defer cancel()
	err := r.store.InsertScanBatch(ctx, domain.ScanBatch{Events: batch, Rollups: BuildRollups(batch)})
	if err != nil {
		r.opts.Logger.Warn("analytics: scan batch insert failed; events dropped", "events", n, "err", err)
		r.dropped.Add(float64(n))
	}
}
