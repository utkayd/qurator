package export

import (
	"context"

	"github.com/utkayd/qurator/internal/domain"
)

// Exporter is an OPTIONAL capability a store.Store driver may additionally implement to
// make itself fully exportable. See the package doc for why it exists (store.Store has
// no bulk-iterate methods) and the Wiring-Needed note asking for it to become
// unnecessary.
//
// Every method streams via a callback rather than returning a slice, so a driver never
// has to materialise its whole table in memory to be exported — matching the
// requirement that Write's own memory use not scale with row count.
type Exporter interface {
	// ExportUsers calls fn once per user, in any order. fn's error aborts the walk and
	// is returned as-is.
	ExportUsers(ctx context.Context, fn func(*domain.User) error) error
	// ExportReservations calls fn once per alias reservation, live or orphaned.
	ExportReservations(ctx context.Context, fn func(ReservationRecord) error) error
	// ExportRollups calls fn once per stored rollup delta (i.e. the aggregate rows
	// data-model.md calls scan_rollups, already collapsed to one row per
	// (code, hour, dimension, value) — not the raw scan_events, which are never
	// exported per FR-055's scope).
	ExportRollups(ctx context.Context, fn func(domain.RollupDelta) error) error
}
