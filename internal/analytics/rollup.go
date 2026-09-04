package analytics

import (
	"sort"
	"time"

	"github.com/utkayd/qurator/internal/domain"
)

// BuildRollups computes the per-batch rollup deltas for events: for every event, one
// increment on the total dimension and one per recorded breakdown dimension, keyed by the
// event's UTC hour. The Recorder hands these to Store.InsertScanBatch alongside the raw
// events so both land in ONE transaction and breakdowns equal totals by construction.
//
// This must stay identical to storetest.BuildRollups, the canonical definition the store
// contract suite asserts against. Production code cannot import a test-support package,
// so rollup_test.go pins equivalence on randomised input instead. Change both or neither.
func BuildRollups(events []domain.ScanEvent) []domain.RollupDelta {
	type key struct {
		code string
		hour time.Time
		dim  domain.Dimension
		val  string
	}
	counts := make(map[key]int64, len(events)*5)
	for _, ev := range events {
		hour := ev.OccurredAt.UTC().Truncate(time.Hour)
		bot := "false"
		if ev.IsBot {
			bot = "true"
		}
		counts[key{ev.CodeID, hour, domain.DimTotal, ""}]++
		counts[key{ev.CodeID, hour, domain.DimUAFamily, ev.UAFamily}]++
		counts[key{ev.CodeID, hour, domain.DimDeviceCategory, string(ev.DeviceCategory)}]++
		counts[key{ev.CodeID, hour, domain.DimReferrerHost, ev.ReferrerHost}]++
		counts[key{ev.CodeID, hour, domain.DimIsBot, bot}]++
	}
	out := make([]domain.RollupDelta, 0, len(counts))
	for k, n := range counts {
		out = append(out, domain.RollupDelta{CodeID: k.code, HourBucket: k.hour, Dimension: k.dim, Value: k.val, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.CodeID != b.CodeID {
			return a.CodeID < b.CodeID
		}
		if !a.HourBucket.Equal(b.HourBucket) {
			return a.HourBucket.Before(b.HourBucket)
		}
		if a.Dimension != b.Dimension {
			return a.Dimension < b.Dimension
		}
		return a.Value < b.Value
	})
	return out
}
