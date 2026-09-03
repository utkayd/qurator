# Implementation Plan: qurator v1 — Self-Hostable QR Service

**Branch**: `001-qr-service-baseline` | **Date**: 2026-09-04 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/001-qr-service-baseline/spec.md`

**Constitution**: v1.0.1 — amended during Phase 0, see Constitution Check below.

## Summary

qurator is a single statically linked Go binary that generates QR codes two ways: an
**ephemeral** path that renders and returns image bytes touching no storage, and a
**dynamic** path that persists a short code whose scan destination the owner can change
after the code has been printed. Scans redirect through `/r/{code}`, unauthenticated and
fast, recording analytics asynchronously so a stalled writer can never slow a redirect.

The architecture is shaped by one constraint above all: a person must be able to run it
with no external services. That makes SQLite and the local filesystem the defaults, with
PostgreSQL and S3-compatible storage as configuration-only upgrades behind interfaces that
a shared contract suite proves interchangeable.

Phase 0 verified every candidate library against real source rather than reputation. Four
of the seven proposed libraries did not survive: `go-pkgz/auth` has no `Bearer` support and
no revocation; `chi` is superseded by stdlib `ServeMux` exposing `r.Pattern`;
`yeqown/go-qrcode`'s SVG writer does not exist; and cross-dialect `.sql` migrations are
provably impossible. Each reversal and its evidence is recorded in
[research.md](./research.md).

## Technical Context

**Language/Version**: Go 1.26 (supports current and previous stable per constitution)

**Primary Dependencies**:

| Dependency | Version | Purpose | Justification (Constitution: Technology Constraints) |
|-----------|---------|---------|------------------------------------------------------|
| `piglig/go-qr` | v1.1.0 | QR encoding, PNG + SVG | Only library with PNG *and* SVG plus logo ECC budgeting; MIT; deterministic output |
| `golang-jwt/jwt/v5` | v5.3.1 | JWT signing/verification | Zero transitive dependencies (verified). Replaces rejected `go-pkgz/auth` |
| `golang.org/x/crypto` | latest | Argon2id for the admin password | Stdlib-adjacent; the one human-chosen secret needs a real KDF |
| `modernc.org/sqlite` | v1.58.0 | Default metadata store | Pure Go — the only way to keep `CGO_ENABLED=0` and a static binary |
| `jackc/pgx/v5` | v5.10.0 | PostgreSQL driver | Used via `database/sql` for one common query layer |
| `pressly/goose/v3` | v3.28.0 | Embedded migrations | Go-based migrations give one ordered sequence across both dialects |
| `minio/minio-go/v7` | v7.3.0 | S3-compatible blob store | 5.72MB vs 7.39MB and 59 vs 113 deps against aws-sdk-go-v2; purpose-built for self-hosted S3 |
| `maypok86/otter/v2` | latest | Scan resolution cache | 3.9ns/op zero-alloc on our skewed hot-key workload; bounded memory |
| `medama-io/go-useragent` | v1.2.4 | UA family + device class | 314ns zero-alloc; ~1.8MB measured binary cost |
| `knadh/koanf/v2` | v2.3.6 | Configuration | Only candidate satisfying flags > env > file > default |
| `prometheus/client_golang` | v1.24.1 | Metrics | Standard; required by Principle VIII |
| `makiuchi-d/gozxing` | v0.1.1 | **Test-only** independent decoder | Principle VII requires decode-based round-trip verification |
| `srwiley/oksvg` + `rasterx` | latest | **Test-only** SVG rasterisation | Lets SVG output be decoded and proven scannable |

Router, HTTP serving, embedding, and logging use the standard library only
(`net/http.ServeMux`, `embed`, `log/slog`).

**Storage**: SQLite (default, embedded) or PostgreSQL, behind `store.Store`; local
filesystem (default) or S3-compatible, behind `blob.BlobStore`.

**Testing**: `go test -race`; shared contract suites per interface; decode-based QR
round-trip verification; benchmarks gated by `benchstat` on the two hot paths.

**Target Platform**: Linux server (amd64, arm64) as a distroless non-root container;
binary also runs on macOS and Windows for local use.

**Project Type**: Single-module web service with a server-rendered console embedded in the
same binary. No separate frontend project and no JavaScript build step.

**Performance Goals**: redirect p99 < 50ms and 1,000 scans/sec (SC-003, SC-004); ephemeral
generation < 20ms and 500/sec (SC-006). Measured headroom: ~2.5ms per render, ~341k
batched analytics rows/sec on SQLite.

**Constraints**: `CGO_ENABLED=0`; starts with zero external services; no exposure-widening
default; no telemetry; no persisted scanner IP addresses; no geographic attribution.

**Scale/Scope**: single instance to ~10M scans/month; single-tenant; ~56 functional
requirements across 7 user stories.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

Evaluated against `.specify/memory/constitution.md` v1.0.0. Each gate names the design
commitment that satisfies it and the check that proves it, so the gate is falsifiable
rather than a declaration of intent.

### I. Self-Hostable By Default (NON-NEGOTIABLE)

- **Gate**: `go build` produces one static binary with `CGO_ENABLED=0`; a run with an
  empty environment serves every capability.
- **Commitment**: pure-Go SQLite driver (no cgo), local filesystem blob store, UI assets
  and migrations in `embed.FS`. No default configuration names an external host.
- **Proof**: a CI job runs the binary in a network-isolated container with no env vars
  and executes the full acceptance suite.
- **Status**: PASS — pending confirmation that the SQLite driver builds with
  `CGO_ENABLED=0` (assigned to storage research).

### II. Pluggable Persistence

- **Gate**: two `Store` drivers and two `BlobStore` drivers pass one shared contract
  suite unmodified; no dialect string or driver error type appears above the driver.
- **Commitment**: `internal/store` and `internal/blob` own the interfaces, the sentinel
  errors, and the contract suites. Drivers translate native errors inward.
- **Proof**: an import-boundary test asserts that no package outside `internal/store/*`
  imports a SQL driver, and none outside `internal/blob/*` imports an S3 client.
- **Status**: PASS.

### III. Two Modes, Strictly Separated

- **Gate**: the ephemeral path cannot reach storage, structurally — not merely by
  convention.
- **Commitment**: `internal/qr` holds pure encode/render functions taking no store
  dependency. The ephemeral handler depends on `internal/qr` and config only. Its
  constructor accepts no `Store` or `BlobStore`, so a storage call is a compile error,
  not a review comment.
- **Proof**: an import-boundary test asserts `internal/qr` imports neither store package;
  a benchmark covers the ephemeral render.
- **Status**: PASS.

### IV. The Public Scan Path Is Sacred (NON-NEGOTIABLE)

- **Gate**: scan and image routes carry no auth middleware; a stalled analytics writer
  does not affect redirect latency; at most one metadata lookup per scan.
- **Commitment**: public routes are mounted in a router group that never has auth
  middleware attached. Analytics ingestion is a non-blocking channel send that drops on a
  full buffer. An in-process cache fronts code resolution.
- **Proof**: a test asserts the public group's middleware chain excludes the auth
  middleware; a test stalls the analytics writer and asserts redirect latency is
  unchanged and the drop counter rises; a test counts store calls per scan.
- **Status**: PASS.

### V. Unified JWT Auth With A Forward-Auth Escape Hatch

- **Gate**: one verification path for cookie and bearer; forward-auth off by default and
  gated on a trusted-source list; conflicting assertions refused.
- **Commitment**: a single middleware extracts a credential from header or cookie and
  resolves it to one identity type. Forward-auth is a distinct, disabled-by-default
  resolver requiring a configured trusted-peer CIDR list.
- **Proof**: a test asserts an identity header from an untrusted peer is ignored
  entirely; a test asserts duplicate conflicting headers are refused rather than
  resolved; a test asserts the header has no effect when the mode is off.
- **Status**: PASS.

### VI. Secure Defaults & 12-Factor Config

- **Gate**: every exposure-widening setting defaults off; startup refuses a missing
  signing secret outside dev mode; no secret is ever printed.
- **Commitment**: a `Secret` type whose string and JSON forms redact; a config test
  enumerating every exposure-widening field and asserting its zero value is the closed
  one.
- **Proof**: a table-driven test over the config struct asserts defaults; a test asserts
  startup fails without a secret; a test formats the whole config with `%v`, `%+v`,
  `%#v`, and JSON and asserts no secret value appears in any.
- **Status**: PASS.

### VII. Test-First For Contracts (NON-NEGOTIABLE)

- **Gate**: contract tests exist and fail before the code that satisfies them.
- **Commitment**: task ordering puts every storage contract suite, HTTP contract test,
  and encode round-trip test before its implementation. Encoding correctness is verified
  by decoding rendered output with an independent decoder, never by byte snapshots.
- **Proof**: task dependencies in `tasks.md` encode the ordering; commit history shows
  the failing test preceding the implementation.
- **Status**: PASS.

### VIII. Operability Is Part Of Done

- **Gate**: structured logs with a request ID, distinct liveness/readiness, RED metrics
  per route, reproducible multi-arch non-root images, graceful shutdown that flushes.
- **Commitment**: these are tasks in the foundational phase, not a polish phase, so no
  feature ships without them.
- **Proof**: a test asserts `/healthz` succeeds while storage is deliberately broken and
  `/readyz` fails; a test asserts shutdown drains in-flight requests and flushes buffered
  events; a release check asserts both architectures run as a non-root user.
- **Status**: PASS.

### Additional constraints checked

- **Metric cardinality**: per-route metrics MUST label by route *pattern*, never the
  concrete path, or `/r/{code}` would create one time series per short code and destroy
  the metrics backend. Called out explicitly because it is easy to get wrong and
  expensive to discover in production.
- **Domain purity**: `internal/domain` imports neither `net/http` nor any SQL package,
  per the Technology Constraints section. Enforced by the same import-boundary test.
- **No telemetry**: a test asserts the binary makes no outbound connection to any
  project-operated host under default configuration.

**Initial gate result: PASS.** No violations requiring Complexity Tracking at this stage.
Re-evaluated after Phase 1 design below.

### Post-Design Re-Evaluation (after Phase 1)

Re-checked after data model and contracts were designed. One principle required an
amendment; the rest hold as designed.

- **Principle II — AMENDED, now PASS.** Phase 0 proved that a single `.sql` migration set
  cannot serve both dialects: goose does not translate SQL, and a SQLite `AUTOINCREMENT`
  migration fails against PostgreSQL with SQLSTATE 42601. The constitution's original
  wording was unsatisfiable. Rather than record a permanent violation or silently
  disregard the clause, the principle was amended to v1.0.1 to require *one ordered
  migration sequence*, explicitly permitting per-dialect DDL branching inside a single
  numbered migration. The protected property — one version ordering, so the backends
  cannot drift — is preserved intact; only the unachievable "identical SQL text" reading
  was dropped.
- **Principle I — PASS, now measured.** `modernc.org/sqlite` cross-built to a genuinely
  static Linux ELF under `CGO_ENABLED=0`, verified with `file`.
- **Principle III — PASS, strengthened.** Enforced structurally: `internal/qr` takes no
  store dependency and the ephemeral handler's constructor accepts neither `Store` nor
  `BlobStore`, so touching storage there is a compile error. An import-boundary test makes
  the rule machine-checked.
- **Principle IV — PASS, strengthened.** The `302` + `no-store` decision came directly from
  this principle: `301`/`308` are heuristically cached indefinitely, which would have
  silently broken both destination changes and scan recording for the most popular codes.
- **Principle VI — PASS with a recorded gap.** The `Secret` type blocks leakage through
  every format verb and `json.Marshal`, but a `string(secret)` cast defeats it. The type
  system cannot close this, so a lint rule is a task rather than an assumption.
- **Principles V, VII, VIII — PASS**, unchanged from the initial gate.

**Result: PASS.** No entries required in Complexity Tracking.

## Project Structure

### Documentation (this feature)

```text
specs/001-qr-service-baseline/
├── plan.md              # This file
├── spec.md              # Feature specification
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/           # Phase 1 output — HTTP contract
│   ├── openapi.yaml
│   ├── store.md         # Store / BlobStore Go interface contracts
│   └── errors.md        # Error shape and stable code catalogue
├── checklists/
│   └── requirements.md  # Spec quality checklist (complete)
└── tasks.md             # Phase 2 — created by /speckit-tasks, NOT by this command
```

### Source Code (repository root)

```text
cmd/
└── qurator/                  # main: wire config → stores → services → server

internal/
├── domain/                   # Pure types. Imports no net/http, no database/sql.
├── qr/                       # Principle III boundary: NO store dependency, ever.
│   ├── encode.go             #   piglig wrapper, boostEcl=false
│   ├── shape.go              #   IN-HOUSE module geometry (square/dot/rounded)
│   ├── render_png.go         #   shape geometry → raster
│   ├── render_svg.go         #   shape geometry → vector (same geometry source)
│   ├── logo.go               #   overlay + automatic EC raising
│   └── policy.go             #   contrast gate, size/duration bounds
├── shortcode/                # generation, alias validation, reserved words
├── store/                    # Store interface + sentinel errors
│   ├── storetest/            #   ONE contract suite, run by both drivers
│   ├── migrations/           #   goose Go migrations, one ordered sequence
│   ├── sqlite/
│   └── postgres/
├── blob/                     # BlobStore interface + sentinel errors
│   ├── blobtest/             #   ONE contract suite, run by both drivers
│   ├── fsblob/
│   └── s3blob/
├── auth/                     # JWT, API tokens, argon2id, forward-auth, middleware
├── analytics/                # non-blocking ingest, batching, rollup, retention, UA
├── httpapi/
│   ├── public/               # scan redirect + image. NO auth middleware mounted.
│   ├── v1/                   # protected REST API
│   └── middleware/           # request ID, logging, metrics, recovery, CSRF
├── console/                  # html/template handlers + embedded htmx assets
├── config/                   # koanf loading, Secret type, validation
├── observability/            # slog, Prometheus, health/readiness
└── export/                   # streaming JSONL export

tests/
├── contract/                 # HTTP contract tests against the OpenAPI document
├── integration/              # cross-component, both storage backends
├── e2e/                      # full lifecycle through the console
└── arch/                     # import-boundary tests enforcing Principles II & III

deploy/
├── Dockerfile                # multi-stage, distroless/static:nonroot
└── compose.yaml              # optional Postgres + MinIO for the upgrade path

.github/workflows/            # build, lint, test, contract tests, bench gate, release
```

**Structure Decision**: single Go module, single deployable binary. The console is
server-rendered Go templates inside `internal/console` rather than a separate frontend
project, because the HttpOnly-cookie auth model removes the reason an SPA would exist —
JavaScript cannot read the token, so a client-side app would need an auth-bootstrap layer
solving a problem server rendering does not have. `internal/` is used throughout because
v1 publishes no importable library surface; `pkg/` is deliberately absent.

Two directories carry constitutional weight and should not be reorganised casually:
`internal/qr` must never gain a storage import (Principle III), and `internal/store` /
`internal/blob` own the only backend-specific code in the tree (Principle II). `tests/arch`
exists to make both machine-checked rather than conventions people remember.

## Phase 2 Delivery Strategy — parallel streams

`/speckit-tasks` will generate the task list. The intended execution shape, requested by
the project owner, is parallel work across git worktrees. It is recorded here because the
sequencing is a design decision, not a scheduling detail.

**Stage 1 — Foundation (SERIAL, single stream, no worktrees).** `go.mod`, `internal/domain`
types, the `Store` and `BlobStore` interfaces with their sentinel errors, both contract
suites, config, the router skeleton with its two route groups, observability, and
migration 0001. Everything downstream imports these.

This stage is deliberately not parallelised. Six agents each inventing a `Store` interface
would produce six incompatible codebases whose reconciliation costs more than writing the
foundation once. Worktrees isolate files; they do not isolate interface decisions.

**Stage 2 — Feature streams (PARALLEL, one worktree each).** Once the interfaces are
frozen, these touch largely disjoint files:

| Stream | Owns | User stories |
|--------|------|--------------|
| A | `internal/qr` — encoding, in-house shapes, logo, policy | US1, US5 |
| B | `internal/store/*` drivers + migrations | US2, US7 |
| C | `internal/auth` | US3 |
| D | `internal/analytics` | US4 |
| E | `internal/console` | US6 |
| F | `deploy/`, `.github/workflows/`, `internal/export` | US7 |

Shared-file contention concentrates in `internal/httpapi` route registration and
`cmd/qurator` wiring. Both are handled by having Stage 1 land the full route table and
wiring with stub handlers, so feature streams fill in implementations rather than adding
lines to the same list.

**Stage 3 — Integration (SERIAL).** Merge, cross-cutting e2e, benchmark gate, release.

## Complexity Tracking

> Fill ONLY if Constitution Check has violations that must be justified.

No violations. The one conflict Phase 0 surfaced (Principle II's migration clause) was
resolved by a PATCH amendment to the constitution rather than by an exception, because the
original wording was factually unsatisfiable rather than merely inconvenient — see the
Post-Design Re-Evaluation above.

Two items are recorded here not as violations but because they are the places this plan
adds work that a reader might expect a dependency to provide:

| Item | Why it exists | Simpler alternative rejected because |
|------|---------------|--------------------------------------|
| In-house module-shape renderer (`internal/qr/shape.go`) | No Go library combines SVG output with dot/rounded module shapes; FR-002 needs both formats and FR-026 needs shapes | Using two libraries (PNG-with-shapes + SVG-without) would make output silently change appearance by format — a real defect for a branding feature |
| ~280-line auth package instead of a library | `go-pkgz/auth` has no `Bearer` support and no revocation, failing FR-031 and FR-035 outright | No other Go package covers cookie+bearer with revocation without an OAuth stack we would never use |
