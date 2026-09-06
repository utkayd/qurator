# Implementation Plan: First-code reliability

**Date**: 2026-09-06 · **Spec**: [spec.md](spec.md)

## Summary and technical context
Retain Go 1.26, stdlib routing, embedded HTMX 2.0.4, vanilla JS, SQLite/filesystem defaults and all existing interfaces. Add a shared domain-level scan-origin parser used by config and code preparation; a typed 503 creation error; shared route-level sign-in limiting; explicit trusted console error fragments; stable document-level events plus idempotent swapped-content initialization. Advance the edit form version from a successful response header.

Browser tests use a separate private `tests/browser` npm package with pinned Playwright (Apache-2.0, Microsoft-maintained), only as a development dependency. Playwright's real browser is justified because HTTP/HTML tests cannot execute HTMX, clipboard promises, CSP, or DOM lifecycle. There is no JS build step or runtime dependency. Local Chrome is available; CI installs Chromium. Fixtures start the compiled Go binary with fresh default storage and loopback ports, and shut it down afterwards. The existing independent decoder verifies downloaded PNG bytes.

## Constitution Check
- I/II: PASS — no runtime dependency or schema change; empty origin still starts; direct generation remains possible; unchanged backend contracts.
- III/IV: PASS — QR engine stays storage-free; scan/image routes remain public; no added scan-path work.
- V/VI: PASS — configured bootstrap only; shared peer throttle; CSP unchanged; no inferred Host origin; bounded forms.
- VII: PASS — regression tests must fail before implementation, including browser tests. HTTP contracts documented with the new error.
- VIII: PASS — existing observability and shutdown retained; separate browser CI job; static build and existing test gates.
Post-design gate: PASS. No constitution amendment or unresolved design question.

## Project structure
Changes: `internal/domain/scan_origin.go`, `internal/config/validate.go`, `internal/codes/service.go`, HTTP router/error mapping, console handler/templates/JS, `cmd/qurator/console_adapters.go`, README and API contracts. New tests in existing Go suites and `tests/browser/`. Browser fixtures compile/use the real server and decoder; they do not use console fakes. Existing uncommitted deployment/healthcheck changes are preserved.

## Delivery and validation
Follow tasks.md: first spec/checklist, then failing Go/browser tests, then fixes, then targeted and full verification. Browser workflow is independently reproducible and does not affect `go test ./...`. Retain existing 50ms scan p99, 60s propagation, and synchronous batch constraints; no change to hot-path architecture requires a new performance design.

## Complexity tracking
No violations. A test-only npm package is the smallest maintained way to validate actual browser behavior; a hand-written DOM fake cannot validate these failures.
