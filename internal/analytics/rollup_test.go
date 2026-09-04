package analytics

import (
	"context"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/store/storetest"
)

func randomEvents(r *rand.Rand, codes []string, n int) []domain.ScanEvent {
	families := []string{"Chrome", "Safari", "Firefox", "unknown", ""}
	devices := []domain.DeviceCategory{domain.DeviceDesktop, domain.DeviceMobile, domain.DeviceTablet, domain.DeviceTV, domain.DeviceBot, domain.DeviceUnknown}
	refs := []string{"", "a.example.com", "instagram.com", "t.co"}
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	out := make([]domain.ScanEvent, n)
	for i := range out {
		loc := time.UTC
		if r.Intn(2) == 0 {
			loc = time.FixedZone("plus3", 3*3600)
		}
		out[i] = domain.ScanEvent{
			CodeID:         codes[r.Intn(len(codes))],
			OccurredAt:     base.Add(time.Duration(r.Intn(72*3600)) * time.Second).In(loc),
			UAFamily:       families[r.Intn(len(families))],
			DeviceCategory: devices[r.Intn(len(devices))],
			ReferrerHost:   refs[r.Intn(len(refs))],
			IsBot:          r.Intn(4) == 0,
		}
	}
	return out
}

// The production BuildRollups must stay in lockstep with storetest.BuildRollups, the
// canonical function the store contract suite asserts against (contracts/store.md).
// Production code cannot import a test-support package, so this test pins equivalence.
func TestBuildRollupsMatchesCanonical(t *testing.T) {
	r := rand.New(rand.NewSource(20260904))
	codes := []string{"cod_a", "cod_b", "cod_c"}
	for round := 0; round < 50; round++ {
		events := randomEvents(r, codes, r.Intn(400))
		got := BuildRollups(events)
		want := storetest.BuildRollups(events)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round %d: BuildRollups diverged from storetest.BuildRollups\n got=%v\nwant=%v", round, got, want)
		}
	}
	if got := BuildRollups(nil); len(got) != 0 {
		t.Fatalf("BuildRollups(nil)=%v, want empty", got)
	}
}

// T070: after inserting a mixed batch, total equals the sum of every dimension's values
// for the same hour (FR-023), and pruning raw events leaves rollups untouched (FR-024).
func TestRollupsInvariantAndSurvivePrune(t *testing.T) {
	ctx := context.Background()
	ms := storetest.NewMemStore()
	c := seedCode(t, ms)

	r := rand.New(rand.NewSource(7))
	events := randomEvents(r, []string{c.ID}, 500)
	batch := domain.ScanBatch{Events: events, Rollups: BuildRollups(events)}
	if err := ms.InsertScanBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}

	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(4 * 24 * time.Hour)
	check := func(label string) {
		t.Helper()
		res, err := ms.QueryAnalytics(ctx, domain.AnalyticsQuery{CodeID: c.ID, From: from, To: to, Bucket: domain.BucketHour})
		if err != nil {
			t.Fatal(err)
		}
		if res.Total != int64(len(events)) {
			t.Fatalf("%s: total=%d, want %d", label, res.Total, len(events))
		}
		for _, dim := range []domain.Dimension{domain.DimUAFamily, domain.DimDeviceCategory, domain.DimReferrerHost, domain.DimIsBot} {
			var sum int64
			for _, v := range res.Breakdowns[dim] {
				sum += v.Count
			}
			if sum != res.Total {
				t.Fatalf("%s: dimension %s sums to %d, total is %d", label, dim, sum, res.Total)
			}
		}
		var series int64
		for _, p := range res.Series {
			series += p.Count
		}
		if series != res.Total {
			t.Fatalf("%s: series sums to %d, total is %d", label, series, res.Total)
		}
	}
	check("before prune")

	// Per-hour: for one hour bucket, the rollup total equals the raw events in that hour.
	hour := events[0].OccurredAt.UTC().Truncate(time.Hour)
	var raw int64
	for _, ev := range events {
		if ev.OccurredAt.UTC().Truncate(time.Hour).Equal(hour) {
			raw++
		}
	}
	res, err := ms.QueryAnalytics(ctx, domain.AnalyticsQuery{CodeID: c.ID, From: hour, To: hour.Add(time.Hour), Bucket: domain.BucketHour})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != raw {
		t.Fatalf("hour %v: rollup total %d, raw events %d", hour, res.Total, raw)
	}

	// Prune everything; the rollups must not move.
	deleted, err := ms.PruneScanEvents(ctx, to.Add(time.Hour), 100000)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != int64(len(events)) {
		t.Fatalf("pruned %d, want %d", deleted, len(events))
	}
	check("after prune")
}
