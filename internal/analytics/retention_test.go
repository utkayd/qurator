package analytics

import (
	"context"
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/store/storetest"
)

// T076: retention prunes raw events older than the window in batches until none remain,
// and rollups survive.
func TestRetentionRunOnce(t *testing.T) {
	ctx := context.Background()
	ms := storetest.NewMemStore()
	c := seedCode(t, ms)
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

	var events []domain.ScanEvent
	for i := 0; i < 25; i++ { // old: 40 days ago
		events = append(events, domain.ScanEvent{CodeID: c.ID, OccurredAt: now.Add(-40*24*time.Hour + time.Duration(i)*time.Minute), UAFamily: "Chrome", DeviceCategory: domain.DeviceDesktop})
	}
	for i := 0; i < 5; i++ { // recent: 2 days ago
		events = append(events, domain.ScanEvent{CodeID: c.ID, OccurredAt: now.Add(-2*24*time.Hour + time.Duration(i)*time.Minute), UAFamily: "Safari", DeviceCategory: domain.DeviceMobile})
	}
	if err := ms.InsertScanBatch(ctx, domain.ScanBatch{Events: events, Rollups: BuildRollups(events)}); err != nil {
		t.Fatal(err)
	}

	ret := NewRetention(ms, 30, 10)
	ret.now = func() time.Time { return now }
	deleted, err := ret.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 25 {
		t.Fatalf("deleted=%d, want 25 (batches of 10 until zero)", deleted)
	}
	deleted, err = ret.RunOnce(ctx)
	if err != nil || deleted != 0 {
		t.Fatalf("second run: deleted=%d err=%v", deleted, err)
	}

	res, err := ms.QueryAnalytics(ctx, domain.AnalyticsQuery{CodeID: c.ID, From: now.Add(-60 * 24 * time.Hour), To: now, Bucket: domain.BucketDay})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 30 {
		t.Fatalf("rollups after prune: total=%d, want 30", res.Total)
	}
}

func TestRetentionDisabled(t *testing.T) {
	ret := NewRetention(storetest.NewMemStore(), 0, 10)
	if n, err := ret.RunOnce(context.Background()); n != 0 || err != nil {
		t.Fatalf("disabled retention: n=%d err=%v", n, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { ret.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return when disabled")
	}
}

func TestRetentionRunStopsOnCancel(t *testing.T) {
	ret := NewRetention(storetest.NewMemStore(), 30, 10)
	ret.interval = time.Hour
	ret.jitter = 0
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { ret.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}
