// Package export implements the whole-instance data dump used by US7 (FR-055, FR-056):
// a streaming tar archive containing one manifest.json plus one <entity>.jsonl file per
// entity, and the reverse import into a fresh store.
//
// Write walks the store with the bulk-iteration methods on store.Store (ForEachUser,
// ForEachCode, ForEachRollup, ForEachReservation) plus ListTokens per user, so every
// driver is fully exportable through the base interface and no optional capability can
// be forgotten. Rows are spooled to temp files as they stream, so qurator's own resident
// memory does not grow with table size.
//
// What the archive carries, and what it deliberately does not:
//   - users without PasswordHash and api_tokens without SecretHash: an export never
//     carries a credential (see manifest.go for the consequences on import);
//   - codes of every user, deleted ones included, with styling inlined;
//   - alias_reservations, released ones included, so the namespace history survives;
//   - scan_rollups (the aggregates), never raw scan_events (out of FR-055's scope).
package export
