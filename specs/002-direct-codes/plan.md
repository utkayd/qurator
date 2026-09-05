# Implementation Plan: Direct Codes

**Branch**: `002-direct-codes` | **Date**: 2026-09-05 | **Spec**: [spec.md](./spec.md)
**Constitution**: v1.0.1 — no principle affected; Principle IV strengthened (fewer scans pass through the instance by choice).

## Summary
Add an immutable `mode` (`dynamic` default, `direct`) to codes. Direct codes encode the destination itself in the image, refuse destination/state mutation with `direct_code_immutable`, and report analytics as `not_tracked`. Existing codes are migrated to `dynamic` explicitly.

## Technical Context
No new dependencies. Touches: `internal/domain` (CodeMode), migration 0003 (`codes.mode TEXT NOT NULL DEFAULT 'dynamic'`), both store drivers + memstore + contract suite, `internal/codes` (content selection, capacity check via `qr` capacity table, mutation refusals), `internal/httpapi/v1/{codes,analytics}.go`, `internal/httpapi/errors.go` (two new codes), OpenAPI, `internal/export` (mode round-trip), console create form + detail page.

## Constitution Check
- II: mode is a column in the one migration sequence; drivers pass the extended contract suite unmodified. PASS.
- III: ephemeral untouched. PASS.
- IV: `/r/{code}` behaviour unchanged for both modes. PASS.
- VII: contract tests for `mode`, `direct_code_immutable`, `not_tracked`, and decode-to-destination precede implementation. PASS.

## Structure
No new packages. Migration `internal/store/migrations/0003_code_mode.go`.

## Tasks
- [x] T201 [P] Contract tests: `tests/contract/codes_test.go` — create with mode direct → image decodes to destination (PNG via real renderer + gozxing); default → dynamic, scan_url present; direct → no scan_url; PATCH/disable/enable → 409 `direct_code_immutable`; over-capacity destination at EC-H → 413 `content_too_large`; export includes mode
- [x] T202 [P] Contract tests: `tests/contract/analytics_test.go` — direct code → 400 `not_tracked`
- [x] T203 [P] Store contract: `storetest` Req14 mode persists and round-trips, defaults dynamic; memstore + sqlite + postgres
- [x] T204 Migration 0003 adding `mode` with default `'dynamic'` (both dialects) and backfilling existing rows
- [x] T205 `domain.CodeMode`, `Code.Mode`; `codes.Service`: content = destination for direct; capacity check; `ErrDirectImmutable` on UpdateDestination/SetState for direct
- [x] T206 `httpapi` error codes `direct_code_immutable` (409) and `not_tracked` (400); v1 codes + analytics handlers; OpenAPI + errors.md
- [x] T207 Export/import round-trips mode
- [x] T208 [P] Console: mode radio on create form with consequence text; detail page shows mode, hides edit/disable for direct, replaces analytics with explanation; e2e test
- [x] T209 Backend-parity matrix gains a direct-code step; full suite; lint; quickstart addendum
