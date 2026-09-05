// Package analytics is the scan-event pipeline behind User Story 4: a non-blocking
// Recorder that batches events into the store together with their rollup deltas, the
// user-agent and referrer reducers that produce those events, and the retention job that
// prunes raw events while leaving aggregates alone.
//
// Constraints this package upholds by construction:
//
//   - Record never blocks and never allocates. A full buffer drops the event and counts
//     it (FR-020, FR-021, Principle IV). Zero-alloc is benchmark-asserted.
//   - No scanner address is ever accepted, let alone stored: domain.ScanEvent has no such
//     field and nothing here reads one (FR-022, standing rule 8).
//   - Referrers are reduced to a lowercase host; path and query never leave the request
//     (ReferrerHost).
//   - There is no geographic attribution of any kind (FR-025, standing rule 9).
//   - Rollups are computed per batch with BuildRollups and persisted in the same store
//     transaction as the raw events, so breakdowns equal totals (FR-023). BuildRollups is
//     kept identical to storetest.BuildRollups by test, since production code must not
//     import test support.
package analytics
