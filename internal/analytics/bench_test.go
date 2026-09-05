package analytics

import (
	"context"
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/store"
)

// T078: BenchmarkRecord must be zero-alloc on the fast path. It benchmarks the drop path
// (buffer full behind a wedged sink) because that is the path that must never regress: a
// saturated writer is exactly when Record's cost matters to redirect latency.
func BenchmarkRecord(b *testing.B) {
	bs := newBlockingStore()
	u := &domain.User{ID: "usr_b", Email: "b@example.com"}
	if err := bs.CreateUser(context.Background(), u); err != nil {
		b.Fatal(err)
	}
	c := &domain.Code{ID: "cod_bench000000001", ShortCode: "bench", UserID: u.ID, Destination: "https://example.com"}
	if err := bs.CreateCode(context.Background(), c); err != nil {
		b.Fatal(err)
	}
	dropped := &countingCounter{}
	rec := NewRecorder(bs, Options{BufferSize: 16, BatchSize: 1000, FlushInterval: time.Hour, Workers: 1}, dropped)
	defer func() {
		close(bs.release)
		_ = rec.Close(context.Background())
	}()

	ev := domain.ScanEvent{CodeID: c.ID, OccurredAt: time.Now(), UAFamily: "Chrome", DeviceCategory: domain.DeviceDesktop}
	for bs.calls.Load() == 0 {
		rec.Record(ev)
		time.Sleep(time.Millisecond)
	}
	for i := 0; i < 64; i++ {
		rec.Record(ev)
	}
	if dropped.Load() == 0 {
		b.Fatal("drop path not reached")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec.Record(ev)
	}
}

// BenchmarkRecordAccepted measures the accept path: the buffer drains into a store that
// accepts instantly, so most sends succeed.
func BenchmarkRecordAccepted(b *testing.B) {
	rec := NewRecorder(discardStore{}, Options{BufferSize: 10_000, BatchSize: 200, FlushInterval: 500 * time.Millisecond, Workers: 4}, &countingCounter{})
	defer rec.Close(context.Background()) //nolint:errcheck
	ev := domain.ScanEvent{CodeID: "cod_x", OccurredAt: time.Now(), UAFamily: "Chrome", DeviceCategory: domain.DeviceDesktop}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec.Record(ev)
	}
}

func BenchmarkClassify(b *testing.B) {
	cl := NewClassifier()
	const ua = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = cl.Classify(ua)
	}
}

// discardStore accepts every batch instantly; only InsertScanBatch is ever called.
type discardStore struct{ store.Store }

func (discardStore) InsertScanBatch(context.Context, domain.ScanBatch) error { return nil }
