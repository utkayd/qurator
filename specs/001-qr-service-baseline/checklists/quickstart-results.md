# Quickstart Validation Results (T109)

**Date**: 2026-09-05 · **Build**: `001-qr-service-baseline` at Stage 3 · **Host**: macOS arm64, Go 1.26.5
**Backends**: SQLite + filesystem (default), then PostgreSQL 16 + MinIO via `deploy/compose.yaml`

Each scenario in `quickstart.md` was executed against the real binary (`make build`,
`CGO_ENABLED=0`) or by the integration test that automates it. "Automated by" names the
test that now guards the scenario permanently.

| # | Scenario | SQLite+fs | Postgres+S3 | Automated by |
|---|----------|-----------|-------------|--------------|
| 1 | Zero-configuration start | PASS | n/a | `TestZeroConfig_*` |
| 2 | Ephemeral generation, nothing stored | PASS — `hello world` decoded by gozxing; store and blob dir empty | n/a (path touches no storage) | `tests/contract/qr_test.go` |
| 3 | Change a printed code's destination | PASS — `302`, exact `Cache-Control: no-store, no-cache, must-revalidate`, new `Location` after `PATCH` with `If-Match: "1"`; stale `If-Match` → `409` with `details.actual` | PASS | `TestBackends_IdenticalLifecycle` |
| 4 | Security properties | PASS — `unsupported_scheme`, `self_referential_destination`, `alias_reserved`, forward-auth untrusted peer ignored, duplicate headers refused | PASS | `tests/contract/{codes,auth}_test.go`, `internal/auth/forwardauth_test.go` |
| 5 | Analytics never block a redirect | PASS — 10,000 redirects with the writer wedged: all `302`, p99 **418 µs** (539 µs under `-race`), 9,860 events dropped and counted, ≤49 store lookups | n/a (in-process) | `TestStall_AnalyticsNeverBlocksRedirect` |
| 5b | Privacy: no address anywhere | PASS — 9 tables, 5,127 stored values, 1,000 scan rows: no IPv4/IPv6, no `token=`, referrer host only | PASS | `TestPrivacy_NoAddressPersisted_{SQLite,Postgres}` |
| 6 | Styling stays scannable | PASS — every shape × EC × size decodes; contrast, logo, dimension rejections fire | n/a | `internal/qr/*_test.go` |
| 7 | Backend swap changes nothing | — | PASS — identical 12-step (status, error code) sequence on both backends | `TestBackends_IdenticalLifecycle` |
| 7b | Export / import | PASS — tar with manifest + JSONL per entity; re-import into a fresh store equal | PASS | `internal/export/export_test.go` |
| 8 | Shutdown loses nothing | PASS — every request the server read completed; post-restart analytics total equals the exact number of `302`s served before SIGTERM | n/a | `TestShutdown_RealBinary` |

## Full suite against live backends

```
QURATOR_TEST_PG_DSN=postgres://qurator:qurator@localhost:5432/qurator?sslmode=disable \
QURATOR_TEST_S3_ENDPOINT=localhost:9000 QURATOR_TEST_S3_ACCESS_KEY=qurator \
QURATOR_TEST_S3_SECRET_KEY=qurator-dev-secret go test -race -count=1 ./...
```

All 22 test packages `ok`, with the PostgreSQL and S3 contract suites, migrations, the
backend-parity matrix, and the PostgreSQL privacy dump executing rather than skipping.

## Measured against success criteria

| Criterion | Target | Measured |
|-----------|--------|----------|
| SC-003 redirect p99 | < 50 ms | 0.42 ms (writer wedged) |
| SC-006 ephemeral render | < 20 ms | 3.1 ms PNG / 0.25 ms SVG at 512 px |
| SC-011 revocation propagation | ≤ 60 s | 30 s cache TTL |
| SC-013 export round-trip | identical | identical (memstore) |
| SC-014 unflushed events on SIGTERM | 0 | 0 |
| Analytics `Record` on full buffer | non-blocking | 7.5 ns, 0 allocs |

## Deviations recorded

- **Scenario 1 as originally written contradicted FR-040** (empty environment must start
  vs. must refuse without a signing secret). Resolved per `/speckit-analyze` C1 by
  generating and persisting `data/signing.key` on first start; FR-040 reworded.
- **Shutdown**: connections still in the kernel accept queue when the listener closes
  are never seen by the HTTP server and surface client-side as `EOF`. These are counted
  as refused, not as drain failures; the drain guarantee covers requests the server read.
  Production deployments cover the window with a readiness flip and a pre-stop delay.
- **PNG determinism across architectures**: dot/rounded modules use anti-aliasing whose
  float results may differ between amd64 and arm64, so the same instance always yields
  the same `ETag` but two architectures may not. Square modules and all SVG are
  integer-only and identical everywhere.
- Local `golangci-lint` (built with Go 1.25) cannot lint a Go 1.26 module; CI runs the
  current release.
