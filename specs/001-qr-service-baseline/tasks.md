---
description: "Task list for qurator v1 — three-stage delivery with six parallel worktree streams"
---

# Tasks: qurator v1 — Self-Hostable QR Service

**Input**: Design documents from `/specs/001-qr-service-baseline/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/, quickstart.md

**Tests**: Per Constitution Principle VII (Test-First For Contracts, NON-NEGOTIABLE), test
tasks are MANDATORY for contract surfaces — QR encoding/rendering, storage drivers, and
HTTP endpoint contracts — and are ordered before the tasks that implement them. Tests for
internal helpers are included only where they pin a security or performance property.

**Organization**: Three delivery stages per plan.md. Stage 1 is serial. Stage 2 is six
parallel streams, each owning disjoint packages, executed in separate git worktrees.
Stage 3 is serial integration.

## Format: `[ID] [P?] [Story?] (Stream X) Description with file path`

- **[P]**: Can run in parallel — different files, no dependency on an incomplete task
- **[USn]**: User story from spec.md; foundation and integration tasks carry none
- **(Stream X)**: Worktree assignment for Stage 2. Foundation tasks are `(Stream 0)`, meaning
  the single serial stream on the feature branch itself

## Stream ownership (Stage 2)

| Stream | Owns exclusively | Stories | Worktree branch |
|--------|------------------|---------|-----------------|
| A | `internal/qr/`, `internal/httpapi/v1/qr.go`, `tools/qrdecode/` | US1, US5 | `stream/a-qr` |
| B | `internal/store/sqlite/`, `internal/store/postgres/`, `internal/store/migrations/`, `internal/blob/fsblob/`, `internal/blob/s3blob/`, `internal/httpapi/v1/codes.go`, `internal/httpapi/public/` | US2, US7 | `stream/b-store` |
| C | `internal/auth/`, `internal/httpapi/v1/auth.go`, `internal/httpapi/v1/tokens.go`, `internal/httpapi/v1/admin.go` | US3 | `stream/c-auth` |
| D | `internal/analytics/`, `internal/httpapi/v1/analytics.go` | US4 | `stream/d-analytics` |
| E | `internal/console/` | US6 | `stream/e-console` |
| F | `deploy/`, `.github/workflows/contract-tests.yml`, `.github/workflows/bench.yml`, `.github/workflows/release.yml`, `internal/export/`, `internal/httpapi/v1/export.go`, `README.md` | US7 | `stream/f-ops` |

**Contention rule**: a stream that must change a file outside its ownership (most likely
`cmd/qurator/main.go` wiring or `internal/httpapi/router.go`) does NOT edit it. It records
the needed change as a one-line note in its final commit message and the change is applied
in Stage 3 (T098). Foundation lands the full route table and wiring as stubs precisely so
this is rare.

## Path Conventions

Single Go module at the repository root. Module path `github.com/utkay/qurator`. Layout
per plan.md: `cmd/qurator/`, `internal/<package>/`, `tests/{arch,contract,integration,e2e}/`,
`deploy/`, `.github/workflows/`.

---

# STAGE 1 — FOUNDATION (serial, Stream 0, on branch `001-qr-service-baseline`)

## Phase 1: Setup

**Purpose**: A compiling module with tooling, so every later task starts from green.

- [ ] T001 (Stream 0) Initialise module `github.com/utkay/qurator` with `go mod init`, Go 1.26 directive, and create the directory skeleton from plan.md (`cmd/qurator`, every `internal/*` package dir with a `doc.go`, `tests/{arch,contract,integration,e2e}`, `deploy`, `.github/workflows`)
- [ ] T002 [P] (Stream 0) Add `Makefile` with targets `build` (`CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/qurator ./cmd/qurator`), `test` (`go test -race ./...`), `lint`, `bench`, `fmt-check` (`test -z "$(gofmt -l .)"`)
- [ ] T003 [P] (Stream 0) Add `.golangci.yml` enabling `govet`, `staticcheck`, `errcheck`, `gosec`, `forbidigo` (forbid `fmt.Print*` outside `cmd/` and `tools/`, per Principle VIII), and `depguard` denying `github.com/go-pkgz/auth`, `github.com/go-chi/chi`, `github.com/yeqown/go-qrcode`, `github.com/mattn/go-sqlite3` (the four rejected libraries in research.md)
- [ ] T004 [P] (Stream 0) Add `.github/workflows/ci.yml` running `make build`, `go vet ./...`, `make fmt-check`, `golangci-lint`, and `go test -race ./...` on push and PR — with NO service containers (that is `contract-tests.yml`, Stream F), so this job is the Docker-free baseline every stream inherits
- [ ] T005 [P] (Stream 0) Add `cmd/qurator/main.go` that parses config, prints version on `--version`, and exits — enough to make `make build` produce a binary before any feature exists

## Phase 2: Foundational (blocking prerequisites)

**Purpose**: The interfaces, types, and skeleton every Stage 2 stream imports. These are
frozen at the end of this phase; a stream needing to change one stops and raises it rather
than editing it in a worktree.

**⚠️ CRITICAL**: No Stage 2 worktree is created until every task here is merged and
`go test -race ./...` is green.

### Domain and interfaces

- [ ] T006 (Stream 0) Create `internal/domain/types.go` with `User`, `APIToken`, `Code`, `CodeState` (`active`/`disabled`/`deleted`), `Styling` (fields per data-model.md incl. both `ECLevel` and `ECLevelEffective`), `ScanEvent` (NO IP field — assert in a comment that one must never be added), `AnalyticsQuery`, `AnalyticsResult`, `CodeFilter` with cursor pagination. Zero imports of `net/http` or `database/sql`
- [ ] T007 [P] (Stream 0) Create `internal/domain/ids.go` with prefixed opaque ID generation (`usr_`, `tok_`, `cod_` + 16 lowercase Crockford base32 chars from `crypto/rand`) and a test in `internal/domain/ids_test.go` asserting length, alphabet, and uniqueness over 100k draws
- [ ] T008 [P] (Stream 0) Create `internal/store/store.go` with the `Store` interface exactly as in `contracts/store.md` (incl. `UpdateDestination(..., expectedVersion int64)` and `ReleaseAlias`) and `internal/store/errors.go` with `ErrNotFound`, `ErrConflict`, `ErrAliasTaken`
- [ ] T009 [P] (Stream 0) Create `internal/blob/blob.go` with the `BlobStore` interface and `BlobInfo` from `contracts/store.md`, `internal/blob/errors.go` with `ErrBlobNotFound`, and `internal/blob/key.go` with `ValidateKey` rejecting `../`, absolute paths, and NUL — plus `internal/blob/key_test.go`

### Contract suites (tests first — Principle VII)

- [ ] T010 (Stream 0) Write `internal/store/storetest/contract.go` exporting `RunStoreContract(t *testing.T, newStore func(t *testing.T) store.Store)` implementing ALL 12 contract requirements in `contracts/store.md` §Store as subtests — including the deleted-alias-stays-reserved test, the non-owner-returns-`ErrNotFound` test, the same-second optimistic-concurrency test, and the rollups-equal-raw-counts invariant. This file is the specification the two drivers are built against
- [ ] T011 [P] (Stream 0) Write `internal/blob/blobtest/contract.go` exporting `RunBlobContract(t *testing.T, newBlob func(t *testing.T) blob.BlobStore)` implementing all 7 requirements in `contracts/store.md` §BlobStore, including the concurrent-`Put`-leaves-no-partial-file test
- [ ] T012 [P] (Stream 0) Write `internal/store/storetest/memstore.go`, an in-memory `Store` used ONLY by tests of packages above the store layer, and `internal/store/storetest/memstore_test.go` running `RunStoreContract` against it — proving the suite itself is satisfiable before any real driver exists

### Short codes

- [ ] T013 [P] (Stream 0) Create `internal/shortcode/generate.go` (12-char lowercase Crockford base32 from `crypto/rand`), `internal/shortcode/alias.go` (`ValidateAlias`: 3–64 chars, `[a-z0-9-]`, alphanumeric ends, no `--`, lowercase normalisation, rejects generated shape `^[0-9a-hjkmnp-tv-z]{12}$`), and `internal/shortcode/reserved.go` (the reserved-word map from research.md §7)
- [ ] T014 [P] (Stream 0) Write `internal/shortcode/shortcode_test.go` covering alphabet exclusion of `i l o u`, alias rule table (valid/invalid pairs incl. `xn--`, `Spring-Sale` → `spring-sale`), reserved-word rejection, and generated-shape rejection

### Configuration

- [ ] T015 (Stream 0) Create `internal/config/secret.go` with `type Secret string` implementing `String()`, `GoString()`, `MarshalJSON()`, `MarshalText()` all returning `"***"`, and `internal/config/secret_test.go` asserting no leak through `%v %+v %#v %s %q`, `fmt.Println`, `json.Marshal`, and `slog` attribute rendering
- [ ] T016 (Stream 0) Create `internal/config/config.go` using `knadh/koanf/v2` with providers layered default → file → env (`QURATOR_` prefix) → flags, and a `Config` struct covering: listen addr, metrics addr (default `127.0.0.1:9090`, off unless set), DB driver/DSN (default sqlite `./data/qurator.db`), blob driver/path/S3 settings, signing secret, dev mode, bootstrap admin email/password, ephemeral-public toggle + rate limit, forward-auth enable/header/trusted CIDRs, destination scheme allow-list (default `http,https`), fallback destination, render bounds (max px, max ms, max payload bytes), scan retention days (365), base URL
- [ ] T017 (Stream 0) Create `internal/config/validate.go` enforcing: refuse start when signing secret empty and dev mode false (FR-040); refuse start when forward-auth enabled and trusted CIDR list empty (fail closed, research §2); every exposure-widening field defaults off — and `internal/config/validate_test.go` with a table-driven test over each rule plus a test that formats the whole `Config` with `%+v` and JSON and asserts the secret value is absent
- [ ] T018 [P] (Stream 0) Add `.golangci.yml` rule (forbidigo pattern) flagging `string(` applied to a `config.Secret` — the one leak the type cannot prevent (research §6) — and a fixture under `internal/config/testdata/` proving the lint fires

### Observability

- [ ] T019 [P] (Stream 0) Create `internal/observability/log.go` — `slog` JSON/text handler by config, request-ID generation, `WithRequestID(ctx)`/`FromContext(ctx)` helpers, and a handler wrapper that injects the request ID into every record automatically
- [ ] T020 [P] (Stream 0) Create `internal/observability/metrics.go` — Prometheus registry; per-route RED histogram labelled by `r.Pattern` (NEVER `r.URL.Path`) with buckets `{.001,.002,.005,.01,.02,.03,.05,.075,.1,.25,.5,1}`; counters `qurator_generations_total`, `qurator_scans_total`, `qurator_scan_events_dropped_total`; and `internal/observability/metrics_test.go` asserting that 100 requests to `/r/{different codes}` produce exactly ONE label series
- [ ] T021 [P] (Stream 0) Create `internal/observability/health.go` — `/healthz` returning 200 with no dependency calls, `/readyz` calling `Ping` on configured `Store` and `BlobStore` — and `internal/observability/health_test.go` asserting `/healthz` is 200 while a stub store's `Ping` returns an error and `/readyz` is 503

### HTTP skeleton

- [ ] T022 (Stream 0) Create `internal/httpapi/errors.go` with the single error envelope from `contracts/errors.md`, `WriteError(w, code, status, msg, details)`, the 19-code enum as constants, and a test asserting no `error.Error()` string from a wrapped driver error is ever written to the body
- [ ] T023 (Stream 0) Create `internal/httpapi/middleware/` with `requestid.go`, `logging.go`, `metrics.go`, `recover.go` (panic → `internal` error, never a stack in the body), `csrf.go` (require `X-Qurator-Requested-With` on mutating cookie-authenticated requests; bearer-authenticated requests exempt), and `ratelimit.go` (token bucket keyed by client IP used transiently and never logged or stored)
- [ ] T024 (Stream 0) Create `internal/httpapi/router.go` building TWO `http.ServeMux` groups: `public` (mounted at `/r/`, `/i/`, `/healthz`, `/readyz` with request-ID, metrics, recover ONLY — NO auth middleware, ever) and `protected` (mounted at `/v1/` with the full chain incl. an `auth.Middleware` interface value injected from outside). Register EVERY path in `contracts/openapi.yaml` (all 17) with a `notImplemented` stub handler returning `501` + the error envelope
- [ ] T025 (Stream 0) Write `internal/httpapi/router_test.go` asserting (a) every OpenAPI path × method resolves to a handler (parse `contracts/openapi.yaml` in the test), (b) the `public` group's middleware chain does not contain the auth middleware type, (c) `/r/x` and `/i/x.png` reach handlers with no `Authorization` header and no cookie
- [ ] T026 (Stream 0) Create `internal/httpapi/public/redirect.go` and `internal/httpapi/public/image.go` as stubs, `internal/httpapi/v1/{qr,codes,auth,tokens,admin,analytics,export}.go` as stubs, and `internal/console/handler.go` as a stub — one file per Stage 2 stream, each containing only its handler struct + constructor signature, so streams fill files rather than fight over `router.go`

### Migrations scaffold

- [ ] T027 (Stream 0) Create `internal/store/migrations/migrations.go` with the goose Go-migration registration pattern (`goose.AddMigrationContext`), a `Dialect` enum, an `Apply(ctx, db, dialect)` entry point, and `internal/store/migrations/0001_initial.go` creating ALL tables from data-model.md (`users`, `api_tokens`, `sessions`, `codes` incl. `version`, `alias_reservations`, `styling_profiles`, `scan_events`, `scan_rollups`) with the per-dialect branches for identity columns and case-insensitive unique indexes in the SAME numbered migration (Constitution v1.0.1)

### Wiring and architecture tests

- [ ] T028 (Stream 0) Expand `cmd/qurator/main.go` to: load+validate config → open `Store` and `BlobStore` by driver name via factory functions in `internal/store/open.go` and `internal/blob/open.go` (which return `ErrUnknownDriver` for anything not yet registered) → run migrations → build router with stub handlers → start metrics listener if configured → serve → on `SIGTERM`/`SIGINT` run `Shutdown` with 15s budget, then call an `analytics.Flusher` interface with 5s budget (research §6 ordering)
- [ ] T029 [P] (Stream 0) Write `tests/arch/boundaries_test.go` using `golang.org/x/tools/go/packages` to assert: `internal/qr` imports neither `internal/store` nor `internal/blob` (Principle III); `internal/domain` imports neither `net/http` nor `database/sql`; no package outside `internal/store/{sqlite,postgres}` imports `modernc.org/sqlite` or `jackc/pgx`; no package outside `internal/blob/s3blob` imports `minio-go`
- [ ] T030 [P] (Stream 0) Write `tests/arch/routes_test.go` asserting every route registered in `router.go` appears in `internal/shortcode/reserved.go` — the reserved-word list rots silently otherwise (research §7)
- [ ] T031 (Stream 0) Run `make build test lint`, confirm `env -i PATH=$PATH ./bin/qurator` starts and serves `/healthz` with an empty environment (quickstart Scenario 1 — will fail until T028's sqlite factory has a driver; for now assert it fails with `ErrUnknownDriver: sqlite` and a clear message), commit, and **tag `foundation-frozen`**

**Checkpoint**: Interfaces frozen. Create the six worktrees from this tag.

---

# STAGE 2 — PARALLEL FEATURE STREAMS (one worktree each)

## Phase 3: User Story 1 — Generate a QR code instantly, storing nothing (Priority: P1) 🎯 MVP

**Stream A** · `git worktree add ../qurator-a stream/a-qr`

**Goal**: `GET/POST /v1/qr` returns a PNG or SVG that decodes to the input, with no
store or blob dependency anywhere in the call graph.

**Independent Test**: Start with no storage drivers registered; request a QR for `"hello"`;
decode it with gozxing; assert equality; assert the store and blob factories were never
called (quickstart Scenario 2).

### Tests for User Story 1 (REQUIRED for contract surfaces — see Principle VII) ⚠️

- [ ] T032 [P] [US1] (Stream A) Create `tools/qrdecode/main.go` — a CLI wrapping `makiuchi-d/gozxing` that decodes a PNG (and an SVG, via `oksvg`+`rasterx` rasterisation) to stdout, reading bytes via `ResultMetadataType_BYTE_SEGMENTS`; used by quickstart and by the tests below
- [ ] T033 [P] [US1] (Stream A) Write `internal/qr/encode_test.go` round-trip table: ASCII, emoji, RTL Arabic, a 2,953-byte payload at EC-L, a 1,273-byte payload at EC-H, raw `[]byte{0x00, 0xFF, 0x80}` — each rendered to PNG and SVG and decoded with gozxing, asserting exact equality; plus a determinism test (`bytes.Equal` across two renders) and an over-capacity test expecting `ErrContentTooLarge`
- [ ] T034 [P] [US1] (Stream A) Write `internal/qr/encode_test.go::TestECLevelHonoured` asserting a request for EC-L yields a symbol gozxing reports as EC-L (the `boostEcl` trap in research §1)
- [ ] T035 [P] [US1] (Stream A) Write `tests/contract/qr_test.go` against the OpenAPI contract for `/v1/qr`: PNG and SVG content types, `413 content_too_large`, `401` without credential by default, `200` when `ephemeral.public=true`, `429 rate_limited` after the configured burst, and byte-identical responses for identical params

### Implementation for User Story 1

- [ ] T036 [US1] (Stream A) Create `internal/qr/encode.go` wrapping `piglig/go-qr` `EncodeSegments(..., boostEcl=false)`, exposing `Encode(content []byte, ec ECLevel) (*Symbol, error)` where `Symbol` wraps the module grid; enforce the per-level byte capacity table; return `ErrContentTooLarge` with the limit in the error
- [ ] T037 [US1] (Stream A) Create `internal/qr/render_png.go` and `internal/qr/render_svg.go` rendering square modules only (shapes arrive in US5) from `Symbol`, honouring margin, size, fg/bg colour; PNG via `image/png` with a fixed encoder config for determinism; SVG via a `strings.Builder` with no timestamps or random IDs
- [ ] T038 [US1] (Stream A) Create `internal/qr/policy.go` with `Bounds{MaxPx, MaxDuration, MaxPayload}` applied to every render (`context.WithTimeout` around rendering → `ErrRenderTimeout`; dimension check → `ErrDimensionsExceeded`)
- [ ] T039 [US1] (Stream A) Create `internal/qr/bench_test.go` — `BenchmarkRenderPNG` and `BenchmarkRenderSVG` for a 200-byte payload at 512px; this is the benchmark Principle III requires and Stream F's `bench.yml` gates on
- [ ] T040 [US1] (Stream A) Implement `internal/httpapi/v1/qr.go`: constructor `NewQRHandler(renderer *qr.Renderer, cfg EphemeralConfig)` — signature MUST NOT accept a `Store` or `BlobStore` (Principle III made structural); parse params per OpenAPI `StylingRequest`; when `cfg.Public` is false require auth (via the injected middleware's identity in context) else apply the rate limiter; set `Content-Type`, `Cache-Control: public, max-age=31536000, immutable`, and a content-hash `ETag`
- [ ] T041 [US1] (Stream A) Run T033–T035 green, `go test -race ./internal/qr/... ./tests/contract/...`, `make lint`; commit on `stream/a-qr`

**Checkpoint**: US1 is a shippable ephemeral QR service on its own.

---

## Phase 4: User Story 2 — Change where a printed QR code points (Priority: P2)

**Stream B** · `git worktree add ../qurator-b stream/b-store`

**Goal**: Both `Store` drivers and both `BlobStore` drivers pass the contract suites; codes
CRUD works; `/r/{code}` redirects with `302 no-store` from a cache.

**Independent Test**: Create a code → scan → change destination → scan again → new
destination, image unchanged (quickstart Scenario 3); plus every security rejection in
Scenario 4 except forward-auth.

### Tests for User Story 2 (REQUIRED for contract surfaces — see Principle VII) ⚠️

- [ ] T042 [P] [US2] (Stream B) Write `internal/store/sqlite/sqlite_test.go` calling `storetest.RunStoreContract` against a temp-file SQLite DB (must fail — no driver yet)
- [ ] T043 [P] [US2] (Stream B) Write `internal/store/postgres/postgres_test.go` calling `storetest.RunStoreContract`, `t.Skip` when `QURATOR_TEST_PG_DSN` is unset, each run using a fresh schema (`CREATE SCHEMA test_<rand>` + `search_path`) so parallel CI jobs do not collide
- [ ] T044 [P] [US2] (Stream B) Write `internal/blob/fsblob/fsblob_test.go` and `internal/blob/s3blob/s3blob_test.go` calling `blobtest.RunBlobContract` (S3 skips when `QURATOR_TEST_S3_ENDPOINT` unset)
- [ ] T045 [P] [US2] (Stream B) Write `tests/contract/codes_test.go` against OpenAPI for `/v1/codes*`: create (generated + alias), `409 alias_taken` incl. case variant and deleted-code alias, `409 alias_reserved` for `healthz`, `400 alias_invalid` for a 12-char generated-shape alias, `400 unsupported_scheme` for `javascript:`, `400 self_referential_destination`, list pagination stability, `PATCH` with stale `If-Match` → `409` with current version in `details`, `404` for another user's code (memstore with two users)
- [ ] T046 [P] [US2] (Stream B) Write `tests/contract/redirect_test.go`: `302` + exact `Cache-Control: no-store, no-cache, must-revalidate` + `Location`; disabled/deleted/unknown → `200` HTML landing (or `302` to configured fallback); `GET /i/{id}.png` with `If-None-Match` → `304`; a counting-`Store` wrapper asserting at most ONE store call per scan and ZERO on a warm cache

### Implementation for User Story 2

- [ ] T047 [US2] (Stream B) Implement `internal/store/sqlite/sqlite.go`: open with `_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)`, `SetMaxOpenConns(1)` for writes via a dedicated writer `*sql.DB` and a separate reader pool (research §3 single-writer), `COLLATE NOCASE` lookups, error translation from extended code `2067` → `ErrAliasTaken`/`ErrConflict`; register in `internal/store/open.go`
- [ ] T048 [US2] (Stream B) Implement `internal/store/postgres/postgres.go` via `database/sql` + `pgx/v5/stdlib`, `$n` placeholders, `lower()` alias lookups, SQLSTATE `23505` translation via `errors.As(&pgconn.PgError)`; register in `internal/store/open.go`
- [ ] T049 [US2] (Stream B) Implement `UpdateDestination` in both drivers as `UPDATE codes SET destination=?, version=version+1, updated_at=? WHERE id=? AND user_id=? AND version=?` → `ErrConflict` on zero rows; `DeleteCode` as soft-delete that leaves `alias_reservations` intact; `ReleaseAlias` returning `ErrConflict` unless the owning code is `deleted`
- [ ] T050 [P] [US2] (Stream B) Implement `internal/blob/fsblob/fsblob.go`: hash-prefix sharding, temp-file-in-same-dir → `fsync` → `rename` → `fsync(parent dir)`, `ETag` = hex SHA-256; register in `internal/blob/open.go`
- [ ] T051 [P] [US2] (Stream B) Implement `internal/blob/s3blob/s3blob.go` with `minio-go/v7`, `NoSuchKey` → `ErrBlobNotFound`, path-style option for MinIO/Garage; register in `internal/blob/open.go`
- [ ] T052 [US2] (Stream B) Create `internal/codes/service.go` (new package, owned by B): `Create` (validate destination scheme allow-list + self-reference after URL normalisation per FR-011/012; alias via `shortcode.ValidateAlias` + reserved list, or generate; insert-and-retry-on-`ErrAliasTaken` up to 5× for generated codes only; render image via `qr` and `Put` to blob; `ReserveAlias`), `UpdateDestination`, `SetState`, `Delete`, `List`
- [ ] T053 [US2] (Stream B) Create `internal/codes/cache.go` wrapping `maypok86/otter/v2` (50k entries, 30s TTL) keyed by lowercase short code → `{destination, state}`, with `Invalidate(shortCode)` called by the service on every mutation
- [ ] T054 [US2] (Stream B) Implement `internal/httpapi/v1/codes.go` per OpenAPI: all codes operations, `If-Match` parsing to `expectedVersion`, `409 conflict` with `details.actual`, cursor pagination
- [ ] T055 [US2] (Stream B) Implement `internal/httpapi/public/redirect.go`: cache lookup → store fallback → `302` with the exact no-store headers → non-blocking `analytics.Recorder.Record(event)` call (interface from domain; a no-op recorder until Stream D lands) → unknown/disabled/deleted → landing page template or configured fallback `302`; NEVER read `X-Forwarded-For`; NEVER log the client address
- [ ] T056 [US2] (Stream B) Implement `internal/httpapi/public/image.go`: `Stat` for ETag → `304` on `If-None-Match` match → stream `Get`; `Cache-Control: public, max-age=31536000, immutable`
- [ ] T057 [US2] (Stream B) Run T042–T046 green (Postgres/S3 locally via `deploy/compose.yaml` if present, else skipped), `make lint`; commit on `stream/b-store`

**Checkpoint**: Dynamic codes work on SQLite+fs and (in CI) Postgres+S3 identically.

---

## Phase 5: User Story 3 — Secure the instance and issue revocable credentials (Priority: P3)

**Stream C** · `git worktree add ../qurator-c stream/c-auth`

**Goal**: One JWT verification path for cookie and bearer; bootstrap admin; hashed
revocable tokens; forward-auth that fails closed.

**Independent Test**: Sign in → mint token → call API → revoke → same call refused within
30s; forward-auth header from untrusted peer ignored; duplicate headers refused (quickstart
Scenario 4).

### Tests for User Story 3 (REQUIRED for contract surfaces — see Principle VII) ⚠️

- [ ] T058 [P] [US3] (Stream C) Write `internal/auth/password_test.go`: Argon2id PHC round-trip, wrong password rejected, parameters (t=3, m=64MiB, p=4) encoded in the PHC string, and a timing test that verification of a wrong password is not measurably faster than a right one
- [ ] T059 [P] [US3] (Stream C) Write `internal/auth/token_test.go`: `qur_` prefix + 43-char base64url, SHA-256 stored hash, `subtle.ConstantTimeCompare` used (assert via a counting hash spy), revoked → `ErrTokenRevoked` within one cache TTL, `token_version` bump invalidates a session JWT
- [ ] T060 [P] [US3] (Stream C) Write `internal/auth/forwardauth_test.go`: header from peer outside trusted CIDRs → anonymous; from inside → identity; `X-Forwarded-For` spoofing a trusted IP from an untrusted peer → anonymous; two `X-Forwarded-Email` headers → `401`; comma-joined value → `401`; mode disabled → header ignored entirely
- [ ] T061 [P] [US3] (Stream C) Write `tests/contract/auth_test.go` against OpenAPI: signin sets `HttpOnly; Secure; SameSite=Strict; Path=/` and no `Domain`; `/v1/auth/me` works with cookie AND with bearer; mutating request with cookie but without `X-Qurator-Requested-With` → `403`; same request with bearer and no CSRF header → `200`; `POST /v1/tokens` returns `secret` once and `GET /v1/tokens` never includes it; `DELETE /v1/admin/aliases/{alias}` as non-admin → `403`

### Implementation for User Story 3

- [ ] T062 [US3] (Stream C) Create `internal/auth/password.go` — Argon2id via `x/crypto/argon2.IDKey` with PHC encode/verify (t=3, m=65536, p=4, keyLen=32, 16-byte salt)
- [ ] T063 [P] [US3] (Stream C) Create `internal/auth/apitoken.go` — generate `qur_<base64url(32 bytes)>`, store SHA-256 (optionally HMAC with a configured pepper), verify with constant-time compare, 30s positive TTL cache keyed by token ID, lazy `TouchTokenLastUsed` at most once per minute
- [ ] T064 [P] [US3] (Stream C) Create `internal/auth/jwt.go` — HS256 via `golang-jwt/jwt/v5` with claims `sub`, `jti`, `tv` (token_version), `exp` (12h), `iat`; verify checks `tv` against `users.token_version` through the same TTL cache
- [ ] T065 [US3] (Stream C) Create `internal/auth/middleware.go` — ONE `Authenticate` middleware: read `Authorization: Bearer` first, else the session cookie, else (if forward-auth enabled) the identity header ONLY after `net.SplitHostPort(r.RemoteAddr)` is inside a trusted CIDR; refuse on duplicate/comma-joined identity headers; put `Identity{UserID, IsAdmin, Method}` in context; satisfies the `auth.Middleware` interface the router expects
- [ ] T066 [US3] (Stream C) Create `internal/auth/bootstrap.go` — on start, if `CountUsers()==0` and bootstrap email/password are configured, create the admin; never on a marker file, never when users exist (FR-032)
- [ ] T067 [US3] (Stream C) Implement `internal/httpapi/v1/auth.go` (signin/signout/me), `internal/httpapi/v1/tokens.go` (create/list/revoke), `internal/httpapi/v1/admin.go` (`releaseAlias`, `403` unless `IsAdmin`) per OpenAPI
- [ ] T068 [US3] (Stream C) Run T058–T061 green, `make lint`; commit on `stream/c-auth`

**Checkpoint**: The instance can be safely exposed. No release ships before this merges.

---

## Phase 6: User Story 4 — Understand how a code is performing (Priority: P4)

**Stream D** · `git worktree add ../qurator-d stream/d-analytics`

**Goal**: Non-blocking scan recording with drop-and-count, same-transaction rollups,
chunked retention, and the analytics query endpoint.

**Independent Test**: N scans → total N; breakdowns sum to N; stalled writer → redirect
latency unchanged and drop counter rises (quickstart Scenario 5).

### Tests for User Story 4 (REQUIRED for contract surfaces — see Principle VII) ⚠️

- [ ] T069 [P] [US4] (Stream D) Write `internal/analytics/recorder_test.go`: `Record` returns in <1µs when the buffer is full (benchmark-asserted) and increments the drop counter; a sink that blocks forever does not block `Record`; `Close(ctx)` with a 100ms deadline returns by the deadline with un-flushed count reported
- [ ] T070 [P] [US4] (Stream D) Write `internal/analytics/rollup_test.go` against `storetest` memstore: after inserting a mixed batch, `total` equals the sum of every dimension's values for the same hour — the FR-023 invariant — and after `PruneScanEvents`, rollups are untouched
- [ ] T071 [P] [US4] (Stream D) Write `internal/analytics/ua_test.go`: Googlebot/facebookexternalhit/Slackbot/curl → `bot`; iPhone Safari → `mobile`/`Safari`; referrer `https://a.example.com/path?token=secret` → `referrer_host` = `a.example.com` only
- [ ] T072 [P] [US4] (Stream D) Write `tests/contract/analytics_test.go` against OpenAPI: time range filtering, `bucket=hour|day|week`, breakdown sums equal total, no `ip`/`country`/`geo` key anywhere in the response body (assert by walking the JSON)

### Implementation for User Story 4

- [ ] T073 [US4] (Stream D) Create `internal/analytics/recorder.go` — buffered channel (10,000), `Record(ev)` via `select`/`default` drop path incrementing `qurator_scan_events_dropped_total`, N consumer goroutines batching at 200 or 500ms, `Close(ctx)` closing the channel and waiting on a `WaitGroup` under the ctx deadline; implements both `domain.Recorder` and the `analytics.Flusher` interface `main.go` calls
- [ ] T074 [P] [US4] (Stream D) Create `internal/analytics/ua.go` wrapping `medama-io/go-useragent` → `ua_family`, `device_category` (`desktop/mobile/tablet/tv/bot`), `is_bot`; and `internal/analytics/referrer.go` reducing a referrer to host-only
- [ ] T075 [US4] (Stream D) Create `internal/analytics/rollup.go` — build per-batch `(code, hour, dimension, value) → count` deltas and pass them with the raw batch so `Store.InsertScanBatch` upserts both in ONE transaction (`ON CONFLICT ... DO UPDATE SET count = count + ?`); note this requires the batch shape in `domain.ScanBatch` — if not present, Stream D adds it to `internal/analytics/batch.go` and the store call takes that type
- [ ] T076 [P] [US4] (Stream D) Create `internal/analytics/retention.go` — daily ticker calling `PruneScanEvents(before, 1000)` in a loop until zero rows, with jitter, never touching rollups
- [ ] T077 [US4] (Stream D) Implement `internal/httpapi/v1/analytics.go` per OpenAPI: query rollups (never raw events) for total, series by bucket, and breakdowns per dimension
- [ ] T078 [US4] (Stream D) Add `internal/analytics/bench_test.go` — `BenchmarkRecord` (must be zero-alloc on the fast path); run T069–T072 green, `make lint`; commit on `stream/d-analytics`

**Checkpoint**: Analytics on, redirects untouched.

---

## Phase 7: User Story 5 — Make the code look like it belongs to the brand (Priority: P5)

**Stream A** (continues after Phase 3, same worktree)

**Goal**: Colours, module shapes (in-house), margin, size, EC level, logo overlay with
automatic EC raising, and rejection of unscannable combinations.

**Independent Test**: Every shape × EC × margin × size combination decodes; low contrast
and oversized logo rejected (quickstart Scenario 6).

### Tests for User Story 5 (REQUIRED for contract surfaces — see Principle VII) ⚠️

- [ ] T079 [P] [US5] (Stream A) Write `internal/qr/shape_test.go` — for each of `square`/`dot`/`rounded` at 3 sizes and 4 EC levels, render PNG and SVG, decode both with gozxing, assert equality; assert PNG and SVG derive from the same `Geometry` (a spy counts one geometry computation per render pair)
- [ ] T080 [P] [US5] (Stream A) Write `internal/qr/logo_test.go` — logo at 4% on EC-L decodes; 6% on EC-L → `ErrLogoTooLarge` with `max_scale=0.05`; 20% on EC-L with `auto_raise=true` → effective level H and decodes; logo present in output (pixel sample)
- [ ] T081 [P] [US5] (Stream A) Write `internal/qr/contrast_test.go` — `#FEFEFE` on `#FFFFFF` → `ErrContrastTooLow` with the ratio in details; `#101828` on `#FFFFFF` passes; ratio computed by WCAG relative luminance (table of known pairs)

### Implementation for User Story 5

- [ ] T082 [US5] (Stream A) Create `internal/qr/shape.go` — `Geometry` computed once from `Symbol`: for `square` unit rects; `dot` circles at 0.9 module diameter; `rounded` rects with corner radius chosen by neighbour adjacency so runs join (finder patterns always square for scanner reliability)
- [ ] T083 [US5] (Stream A) Refactor `render_png.go` and `render_svg.go` to consume `Geometry` instead of the raw grid (PNG via `golang.org/x/image/vector` or `image/draw` with anti-aliasing; SVG emitting `<rect>`/`<circle>`/`<path>` elements)
- [ ] T084 [US5] (Stream A) Create `internal/qr/logo.go` — decode PNG/JPEG logo, cap at per-level budget (L 5%, M 12%, Q 20%, H 25%), automatic EC raising when `AutoRaise` and the requested level's budget is exceeded (recording `ECLevelEffective`), composite centred with a 1-module bg-coloured pad
- [ ] T085 [US5] (Stream A) Extend `internal/qr/policy.go` with `ContrastRatio(fg, bg)` (WCAG), hard floor 3:1, configurable default gate 4.5:1 → `ErrContrastTooLow`
- [ ] T086 [US5] (Stream A) Extend `internal/httpapi/v1/qr.go` to accept the full `StylingRequest` (multipart or base64 logo) and map every `qr` error to its `contracts/errors.md` code with `details`
- [ ] T087 [US5] (Stream A) Run T079–T081 green, benchmarks not regressed vs T039 baseline, `make lint`; commit on `stream/a-qr`

---

## Phase 8: User Story 6 — Manage everything without writing a request by hand (Priority: P6)

**Stream E** · `git worktree add ../qurator-e stream/e-console`

**Goal**: Server-rendered console (html/template + htmx + one JS file), strict CSP with
nonces, live preview via `/v1/qr`, no external origins.

**Independent Test**: Full lifecycle through the browser on a fresh instance with outbound
network blocked (quickstart Scenario / US6).

### Tests for User Story 6 (REQUIRED for contract surfaces — see Principle VII) ⚠️

- [ ] T088 [P] [US6] (Stream E) Write `internal/console/csp_test.go` — every console response carries the exact CSP from research §5 with a fresh nonce; every `<script>`/`<style>` in rendered HTML carries that nonce; no `unsafe-inline`/`unsafe-eval`; no `hx-on:` attribute anywhere in templates (grep the embedded FS)
- [ ] T089 [P] [US6] (Stream E) Write `internal/console/offline_test.go` — walk every embedded HTML/CSS/JS asset and assert no `http://`/`https://` URL to a non-self origin (FR-043)
- [ ] T090 [P] [US6] (Stream E) Write `tests/e2e/console_test.go` using `net/http/cookiejar` + `golang.org/x/net/html`: sign in → create styled code → list shows it → edit destination → analytics page renders SVG chart → create token (secret shown once, absent on reload) → revoke → delete with confirmation text mentioning printed codes

### Implementation for User Story 6

- [ ] T091 [US6] (Stream E) Create `internal/console/assets/` with vendored `htmx.min.js` (pinned version, licence file), `app.js` (debounced 150ms live preview calling `/v1/qr` with `AbortController`, memo cache, show-once token copy with clipboard API and DOM removal), `app.css` (light + `prefers-color-scheme: dark`), all under `//go:embed`; serve with fingerprinted paths + `Cache-Control: immutable` and pre-gzipped variants
- [ ] T092 [US6] (Stream E) Create `internal/console/templates/` — `layout.html` (nonce injection), `signin.html`, `codes_list.html`, `code_new.html` (styling controls + preview `<img>`), `code_detail.html` (download, edit destination via htmx `hx-patch` with `If-Match`, analytics SVG chart, delete with confirmation stating printed-code consequence), `tokens.html`
- [ ] T093 [US6] (Stream E) Implement `internal/console/handler.go` — routes under `/ui/`, per-request nonce, `X-Qurator-Requested-With` injected by htmx config (`htmx.config.headers`), calls the same service layer as the API (never duplicates validation), and `internal/console/chart.go` rendering the trend as inline SVG in Go
- [ ] T094 [US6] (Stream E) Run T088–T090 green, `make lint`; commit on `stream/e-console`

---

## Phase 9: User Story 7 — Run it seriously, and leave when you want (Priority: P7)

**Stream F** · `git worktree add ../qurator-f stream/f-ops`

**Goal**: Reproducible multi-arch non-root image, contract-test and benchmark CI, JSONL
export/import, README stating the no-required-services promise.

**Independent Test**: Scenario 7 (backend swap identical) and Scenario 8 (shutdown loses
nothing) from quickstart; `docker run` of the image serves `/healthz` as non-root on both
architectures.

### Tests for User Story 7 (REQUIRED for contract surfaces — see Principle VII) ⚠️

- [ ] T095 [P] [US7] (Stream F) Write `internal/export/export_test.go` — export against memstore produces one JSONL file per entity with a `manifest.json`; import into a fresh memstore yields identical rows; export streams (assert memory does not scale with row count using a 100k-row fixture and `testing.AllocsPerRun`)
- [ ] T096 [P] [US7] (Stream F) Write `tests/integration/shutdown_test.go` — start the server, hold 50 in-flight slow requests + 5,000 buffered scan events, send `SIGTERM`, assert all 50 complete 2xx and the store has 5,000 events

### Implementation for User Story 7

- [ ] T097 [P] [US7] (Stream F) Create `internal/export/export.go` and `import.go` (streaming JSONL per entity + manifest, `ErrConflict` on import into a non-empty store unless `--force`), `internal/httpapi/v1/export.go` (`GET /v1/export`, admin only, `application/x-ndjson` streaming), and `export`/`import` subcommands in a NEW file `cmd/qurator/export_cmd.go` (not `main.go`)
- [ ] T098 [P] [US7] (Stream F) Create `deploy/Dockerfile` — multi-stage, `CGO_ENABLED=0`, `-trimpath -buildid=`, `SOURCE_DATE_EPOCH`, final stage `gcr.io/distroless/static-debian13:nonroot@sha256:<pinned>`; and `deploy/compose.yaml` with Postgres 16 + MinIO for the upgrade path and local contract tests
- [ ] T099 [P] [US7] (Stream F) Create `.github/workflows/contract-tests.yml` — Postgres 16 and MinIO as `services:`, exporting `QURATOR_TEST_PG_DSN`/`QURATOR_TEST_S3_ENDPOINT`, running `go test -race ./...` so the skipped suites execute
- [ ] T100 [P] [US7] (Stream F) Create `.github/workflows/bench.yml` — run `BenchmarkRenderPNG`, `BenchmarkRenderSVG`, `BenchmarkRecord`, and a redirect benchmark with `-count=10` on base and PR, compare with `benchstat`, fail on any non-`~` positive delta
- [ ] T101 [P] [US7] (Stream F) Create `.github/workflows/release.yml` — on tag: `docker buildx --platform linux/amd64,linux/arm64` with `--rewrite-timestamp`, push, then a smoke job that runs the image on both platforms as `nonroot` and curls `/healthz`
- [ ] T102 [P] [US7] (Stream F) Write `README.md` — the one-command start with NO configuration as the first thing on the page (Constitution sync report flags this as pending), the two modes, the upgrade path to Postgres/S3, forward-auth setup with oauth2-proxy/Authelia examples, the `302` and no-IP privacy statements, and the export path
- [ ] T103 [US7] (Stream F) Run T095–T096 green, `make lint`; commit on `stream/f-ops`

---

# STAGE 3 — INTEGRATION (serial, Stream 0, on branch `001-qr-service-baseline`)

## Phase 10: Merge and cross-cutting validation

- [ ] T104 (Stream 0) Merge streams in dependency order: B → C → D → A → F → E (B first because C, D, E build on real drivers; E last because it exercises everything). Resolve any contention notes from stream commit messages in `cmd/qurator/main.go` and `internal/httpapi/router.go`; replace every remaining `notImplemented` stub
- [ ] T105 (Stream 0) Write `tests/integration/backends_test.go` — run the full `tests/contract` suite twice via a matrix: `sqlite+fsblob` and `postgres+s3blob` (skipping the latter without env), asserting identical responses (SC-010)
- [ ] T106 (Stream 0) Write `tests/integration/privacy_test.go` — after 1,000 scans, dump every table on both backends and assert no value matches an IPv4/IPv6 pattern and no column name contains `ip`, `addr`, `geo`, `country` (SC-012)
- [ ] T107 (Stream 0) Write `tests/integration/stall_test.go` — swap in a `Store` whose `InsertScanBatch` blocks forever; run 10,000 redirects with `hey`-equivalent in-process load; assert p99 < 50ms and `qurator_scan_events_dropped_total` > 0 (SC-005, Principle IV)
- [ ] T108 (Stream 0) Write `tests/integration/zeroconfig_test.go` — `exec` the built binary with `env -i PATH=...` in a temp dir, assert it serves `/healthz` and `/v1/qr` (with dev mode? NO — assert it REFUSES with the signing-secret message, then passes with `QURATOR_DEV_MODE=1`) (SC-002, FR-040)
- [ ] T109 (Stream 0) Execute every scenario in `quickstart.md` by hand against the merged build on SQLite+fs, then via `deploy/compose.yaml` on Postgres+S3; record results in `specs/001-qr-service-baseline/checklists/quickstart-results.md`
- [ ] T110 (Stream 0) Run `/speckit-analyze` for cross-artifact consistency, then `/security-review` on the branch; fix findings; ensure `make build test lint bench` green and `tests/arch` green
- [ ] T111 (Stream 0) Open the PR to `main` with the quickstart results linked; tag `v1.0.0-rc1` after merge and confirm `release.yml` publishes a multi-arch image that runs as non-root

---

## Dependencies & Execution Order

### Stage dependencies

- **Stage 1 (T001–T031)**: strictly serial in ID order except tasks marked `[P]`. Ends at
  tag `foundation-frozen`. NOTHING in Stage 2 starts before this tag exists.
- **Stage 2 (T032–T103)**: six worktrees created from `foundation-frozen`. Streams are
  independent of each other. Within a stream, tests precede implementation.
  - Stream A runs Phase 3 then Phase 7 sequentially in one worktree.
  - Stream B's redirect handler (T055) uses a no-op `Recorder` until D merges — by design.
  - Stream C's middleware (T065) satisfies the `auth.Middleware` interface frozen in T024.
  - Stream D's `T075` may need a `ScanBatch` type; it adds it in its own package, never in
    `internal/domain`.
- **Stage 3 (T104–T111)**: serial, after ALL streams have their final commit.

### Within each user story

- Contract tests MUST be written and MUST FAIL before the implementation they cover
  (Principle VII)
- Interfaces before drivers; drivers before services; services before handlers
- A story is complete only when its stream's checkpoint task passes `make lint test`

## Parallel Example: Stage 2 kickoff

```bash
git checkout foundation-frozen
for s in a-qr b-store c-auth d-analytics e-console f-ops; do
  git worktree add "../qurator-${s%%-*}" -b "stream/$s" foundation-frozen
done
# Each worktree gets one agent. Stream A starts T032–T035 (all [P]) together,
# Stream B starts T042–T046 together, and so on. No two streams share a file.
```

## Implementation Strategy

### MVP First (Stage 1 + Stream A Phase 3)

1. Complete Stage 1 → a binary that starts, routes, and stubs every endpoint
2. Complete Phase 3 (US1) → ephemeral QR generation, deployable on its own
3. **STOP and VALIDATE**: quickstart Scenarios 1–2 pass with no storage driver

### Incremental Delivery

Each stream's checkpoint is independently demonstrable. Merge order in T104 is chosen so
every intermediate merge state is runnable: B alone gives dynamic codes behind a proxy; +C
makes it exposable; +D adds analytics; +A adds branding; +F makes it releasable; +E gives
it a face.

## Notes

- 111 tasks: Stage 1 = 31, Stage 2 = 72 (A 22, B 16, C 11, D 10, E 7, F 9 — Stream A is
  the largest because the in-house shape renderer is real work no dependency provides),
  Stage 3 = 8
- Module path `github.com/utkay/qurator` is an assumption — confirm before T001
- Any stream discovering that a frozen interface must change STOPS and reports; it does
  not fork the interface in its worktree
