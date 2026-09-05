package analytics

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/store"
	"github.com/utkayd/qurator/internal/store/storetest"
)

// countingCounter is the minimal Counter used by tests.
type countingCounter struct{ n atomic.Int64 }

func (c *countingCounter) Inc()          { c.n.Add(1) }
func (c *countingCounter) Add(f float64) { c.n.Add(int64(f)) }
func (c *countingCounter) Load() int64   { return c.n.Load() }

// blockingStore embeds a real store but makes InsertScanBatch block until released
// (or forever, if never released). It is the "stalled writer" of quickstart Scenario 5.
type blockingStore struct {
	store.Store
	release chan struct{}
	calls   atomic.Int64
}

func newBlockingStore() *blockingStore {
	return &blockingStore{Store: storetest.NewMemStore(), release: make(chan struct{})}
}

func (b *blockingStore) InsertScanBatch(ctx context.Context, batch domain.ScanBatch) error {
	b.calls.Add(1)
	<-b.release // ignores ctx on purpose: a sink that is truly wedged
	return b.Store.InsertScanBatch(ctx, batch)
}

// failingStore rejects every batch.
type failingStore struct {
	store.Store
	fails atomic.Int64
}

func (f *failingStore) InsertScanBatch(context.Context, domain.ScanBatch) error {
	f.fails.Add(1)
	return errors.New("disk on fire")
}

func seedCode(t *testing.T, s store.Store) *domain.Code {
	t.Helper()
	u := &domain.User{ID: "usr_test", Email: "t@example.com"}
	if err := s.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	c := &domain.Code{ID: "cod_test0000000001", ShortCode: "abc123", UserID: u.ID, Destination: "https://example.com"}
	if err := s.CreateCode(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return c
}

// closeQuietly releases a wedged sink and then drains the recorder, so a failing test
// never hangs in its deferred Close.
func closeQuietly(t *testing.T, rec *Recorder, bs *blockingStore) {
	t.Helper()
	close(bs.release)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rec.Close(ctx); err != nil {
		t.Logf("close: %v", err)
	}
}

func event(codeID string) domain.ScanEvent {
	return domain.ScanEvent{CodeID: codeID, OccurredAt: time.Now().UTC(), UAFamily: "Chrome", DeviceCategory: domain.DeviceDesktop}
}

// T069: Record on a full buffer returns in well under a microsecond and increments the
// drop counter — it never blocks on the sink.
func TestRecordFullBufferDropsFast(t *testing.T) {
	bs := newBlockingStore()
	seedCode(t, bs)
	dropped := &countingCounter{}
	const buf = 64
	rec := NewRecorder(bs, Options{BufferSize: buf, BatchSize: 1000, FlushInterval: time.Hour, Workers: 1}, dropped)
	defer closeQuietly(t, rec, bs)

	ev := event("cod_test0000000001")
	// Fill the buffer. The single worker takes at most one event into its batch and then
	// blocks in the sink, so after buf+1+slack events the channel is full and stays full.
	for i := 0; i < buf+16; i++ {
		rec.Record(ev)
	}
	// Wait until the worker is wedged inside the sink and the buffer behind it is full:
	// only then is every further Record a drop.
	deadline := time.Now().Add(5 * time.Second)
	for bs.calls.Load() == 0 && time.Now().Before(deadline) {
		rec.Record(ev)
		time.Sleep(time.Millisecond)
	}
	if bs.calls.Load() == 0 {
		t.Fatal("worker never reached the sink")
	}
	for i := 0; i < buf+16; i++ {
		rec.Record(ev)
	}
	if dropped.Load() == 0 {
		t.Fatal("buffer never filled; drop path not reached")
	}

	before := dropped.Load()
	const n = 10_000
	start := time.Now()
	for i := 0; i < n; i++ {
		rec.Record(ev)
	}
	elapsed := time.Since(start)
	mean := elapsed / n
	// The spec target is <1µs. We allow generous slack for -race and loaded CI runners:
	// a Record that took anywhere near 5µs would still be four orders of magnitude below
	// the 50ms redirect budget, while a blocking implementation takes seconds here.
	const budget = 5 * time.Microsecond
	if mean > budget {
		t.Fatalf("Record on full buffer: mean %v per call, want < %v (target 1µs)", mean, budget)
	}
	if got := dropped.Load() - before; got != n {
		t.Fatalf("dropped counter rose by %d, want %d", got, n)
	}
	t.Logf("Record on full buffer: mean %v per call", mean)
}

// T069: a sink that blocks forever never blocks Record, and Close with a 100ms deadline
// returns by the deadline reporting the unflushed count.
func TestCloseBoundedByDeadlineWithStalledSink(t *testing.T) {
	bs := newBlockingStore()
	seedCode(t, bs)
	dropped := &countingCounter{}
	rec := NewRecorder(bs, Options{BufferSize: 100, BatchSize: 10, FlushInterval: 10 * time.Millisecond, Workers: 2}, dropped)
	defer closeQuietly(t, rec, bs)

	const accepted = 50
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < accepted; i++ {
			rec.Record(event("cod_test0000000001"))
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Record blocked behind a stalled sink")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := rec.Close(ctx)
	took := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close: err=%v, want DeadlineExceeded", err)
	}
	if took > 500*time.Millisecond {
		t.Fatalf("Close took %v; the deadline was 100ms", took)
	}
	if got := rec.Unflushed(); got <= 0 || got > accepted {
		t.Fatalf("Unflushed()=%d, want in (0, %d]", got, accepted)
	}
	if dropped.Load() != 0 {
		t.Fatalf("no drops expected with a 100-slot buffer and 50 events; got %d", dropped.Load())
	}
	// After Close, Record drops rather than sending on a closed channel.
	rec.Record(event("cod_test0000000001"))
	if dropped.Load() != 1 {
		t.Fatalf("Record after Close: dropped=%d, want 1", dropped.Load())
	}
}

// Happy path: everything recorded lands in the store with matching rollups, across both
// the size and the interval triggers.
func TestRecorderFlushesEverything(t *testing.T) {
	ms := storetest.NewMemStore()
	c := seedCode(t, ms)
	dropped := &countingCounter{}
	rec := NewRecorder(ms, Options{BufferSize: 1000, BatchSize: 7, FlushInterval: 20 * time.Millisecond, Workers: 3}, dropped)

	const n = 100
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < n/4; i++ {
				rec.Record(event(c.ID))
			}
		}()
	}
	wg.Wait()
	if err := rec.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if rec.Unflushed() != 0 {
		t.Fatalf("Unflushed()=%d after clean Close", rec.Unflushed())
	}
	res, err := ms.QueryAnalytics(context.Background(), domain.AnalyticsQuery{
		CodeID: c.ID, From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour), Bucket: domain.BucketHour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != n {
		t.Fatalf("total=%d, want %d (dropped=%d)", res.Total, n, dropped.Load())
	}
	// Closing twice is harmless.
	if err := rec.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// A failing sink counts the batch as dropped, logs, and keeps consuming.
func TestRecorderFailingSinkCountsDropped(t *testing.T) {
	fs := &failingStore{Store: storetest.NewMemStore()}
	c := seedCode(t, fs)
	dropped := &countingCounter{}
	rec := NewRecorder(fs, Options{BufferSize: 100, BatchSize: 5, FlushInterval: 10 * time.Millisecond, Workers: 1}, dropped)
	for i := 0; i < 12; i++ {
		rec.Record(event(c.ID))
	}
	if err := rec.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if dropped.Load() != 12 {
		t.Fatalf("dropped=%d, want 12", dropped.Load())
	}
	if fs.fails.Load() < 3 {
		t.Fatalf("expected >=3 failed batches (5+5+2), got %d", fs.fails.Load())
	}
	if rec.Unflushed() != 0 {
		t.Fatalf("failed batches are dropped, not pending: Unflushed()=%d", rec.Unflushed())
	}
}

func TestOptionsDefaults(t *testing.T) {
	o := Options{}.withDefaults()
	if o.BufferSize != 10_000 || o.BatchSize != 200 || o.FlushInterval != 500*time.Millisecond || o.Workers != 2 {
		t.Fatalf("unexpected defaults: %+v", o)
	}
	var _ domain.Recorder = (*Recorder)(nil)
	var _ Flusher = (*Recorder)(nil)
}
