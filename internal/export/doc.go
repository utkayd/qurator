// Package export implements the whole-instance data dump used by US7 (FR-055, FR-056):
// a streaming tar archive containing one manifest.json plus one <entity>.jsonl file per
// entity, and the reverse import into a fresh store.
//
// # A real Store interface gap
//
// store.Store (internal/store/store.go) has no bulk-iterate methods: ListCodes takes a
// mandatory CodeFilter.UserID, and there is no ListUsers at all. That means a store
// driver cannot be walked end to end through the base interface alone — a full export
// needs to discover every user ID before it can page through that user's codes and
// tokens.
//
// This package works around the gap with an OPTIONAL interface, Exporter, that a driver
// may additionally implement. Write/Read use a type assertion to detect it:
//
//   - When the underlying store implements Exporter, every entity is exported: users
//     (without PasswordHash), api_tokens per user (without SecretHash), codes per user
//     (via the base ListCodes, paginated by cursor — styling is already inlined on
//     domain.Code so no extra call is needed), alias_reservations, and scan_rollups.
//   - When it does not, users/tokens/reservations/rollups cannot be discovered at all
//     (there is no user ID to filter codes by either), so the export contains only
//     manifest.json recording every omitted entity and why. This is a degraded export,
//     not a silent partial one: the manifest is authoritative about what is present, and
//     Read only ever recreates what the manifest says it wrote.
//
// Wiring-Needed: internal/store.Store should grow real bulk-iterate methods —
// ListUsers(ctx, cursor) (users []*domain.User, next string, err error) and an
// admin-scoped ListCodes variant that does not require a UserID filter — so export (and
// any future admin tooling) does not depend on an optional interface a driver might
// forget to implement. Until that lands, every store driver written in Stage 3 MUST
// implement export.Exporter or exports against it will silently degrade to
// manifest-only (see above: even codes need a discovered user ID).
package export
