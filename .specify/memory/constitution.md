<!--
SYNC IMPACT REPORT
==================
Version change: 1.0.0 → 1.0.1 (PATCH)
Amendment (2026-09-04): Principle II's migration clause restated as "one ordered
migration sequence", explicitly permitting per-dialect DDL branching inside a single
numbered migration. PATCH because this clarifies what the principle protects rather than
weakening it: the protected property is a single version ordering, which is preserved.
Forced by a Phase 0 finding — goose does not translate SQL across dialects (its Dialect
setting governs only its own bookkeeping table), proven by running a SQLite-flavoured
AUTOINCREMENT migration against live PostgreSQL and observing SQLSTATE 42601. The
original wording was therefore unsatisfiable for any realistic schema. See
specs/001-qr-service-baseline/research.md section 3.

--- Original ratification report ---
Version change: TEMPLATE (unversioned) -> 1.0.0
Rationale: Initial ratification. All placeholder tokens replaced with concrete,
testable principles for the qurator project. MAJOR bump to 1.0.0 establishes the
first governed baseline.

Principles defined (8, expanded from the template's 5 slots):
  - [PRINCIPLE_1_NAME] → I. Self-Hostable By Default (NON-NEGOTIABLE)
  - [PRINCIPLE_2_NAME] → II. Pluggable Persistence
  - [PRINCIPLE_3_NAME] → III. Two Modes, Strictly Separated
  - [PRINCIPLE_4_NAME] → IV. The Public Scan Path Is Sacred (NON-NEGOTIABLE)
  - [PRINCIPLE_5_NAME] → V. Unified JWT Auth With A Forward-Auth Escape Hatch
  - (added)            → VI. Secure Defaults & 12-Factor Config
  - (added)            → VII. Test-First For Contracts (NON-NEGOTIABLE)
  - (added)            → VIII. Operability Is Part Of Done

Sections filled:
  - [SECTION_2_NAME] → Technology & Architecture Constraints
  - [SECTION_3_NAME] → Development Workflow & Quality Gates
  - [GOVERNANCE_RULES] → Governance

Removed sections: none.

Templates requiring updates:
  - .specify/templates/plan-template.md ......... ✅ reviewed (generic Constitution
    Check gate reads the principles from this file at plan time; no edit needed)
  - .specify/templates/spec-template.md ......... ✅ reviewed (no constitution-
    specific mandatory sections added or removed)
  - .specify/templates/tasks-template.md ........ ✅ UPDATED (template declared
    tests "OPTIONAL - only include them if explicitly requested", which directly
    contradicts Principle VII; test sections and ordering rules now mark contract
    tests mandatory and test-first, with non-contract tests still optional)
  - .claude/skills/speckit-*/SKILL.md ........... ✅ reviewed (agent-generic
    wording; no hardcoded agent names requiring correction)
  - README.md ................................... ⚠ pending (not yet authored;
    must state the SQLite-default / no-required-services promise of Principle I)

Deferred TODOs: none.
-->

# qurator Constitution

qurator is an open-source, self-hostable QR code service written in Go. It generates
QR codes on demand with no persistence, and manages dynamic QR codes whose targets can
change after printing, backed by a pluggable metadata store and an S3-compatible blob
store.

## Core Principles

### I. Self-Hostable By Default (NON-NEGOTIABLE)

A person MUST be able to run a useful qurator instance with one command, one binary,
and zero external services. The default configuration MUST start successfully with no
database server, no object store, and no identity provider reachable.

- The server MUST ship as a single statically linked binary with all assets — web UI,
  migrations, templates — embedded via `embed.FS`. No runtime asset directory.
- SQLite MUST be the default metadata store, and the local filesystem the default blob
  store. Both MUST work with an empty config.
- PostgreSQL and S3-compatible object storage are opt-in upgrades selected by config.
  They MUST NEVER become prerequisites for any core feature.
- Any feature that cannot work on the default stack MUST degrade gracefully with a
  clear log line, not fail startup.

*Rationale:* Self-hosting dies at the dependency list. Every mandatory service is a
reason someone chooses a SaaS instead. The scaling path must be an upgrade, never an
entry fee.

### II. Pluggable Persistence

Persistence MUST sit behind Go interfaces, and every driver MUST be interchangeable
without touching code above it.

- Two interfaces: a metadata `Store` (SQLite, PostgreSQL) and a `BlobStore`
  (filesystem, S3-compatible). Drivers are selected at startup by config only.
- One shared contract test suite MUST exercise every driver of an interface. A new
  driver is not complete until it passes that suite unmodified.
- Backend-specific types, dialects, SQL strings, and error values MUST NOT appear in
  HTTP handlers or domain logic. Drivers MUST translate their errors into the
  package's own sentinel errors.
- Schema migrations MUST be embedded, versioned, forward-only, and applied to both SQL
  backends from **one ordered migration sequence**. A single version sequence is the
  requirement, because it is what prevents the two backends from drifting apart. Where a
  statement cannot be expressed portably — identity columns and case-insensitive unique
  indexes are the known cases — a migration MAY branch its DDL per dialect internally,
  provided both branches live in the same numbered migration and are applied under the
  same version. Two independently numbered migration sets are prohibited.

*Rationale:* The contract suite is what makes "pluggable" a fact rather than an
aspiration, and it is what keeps a bug fixed in SQLite from silently persisting in
PostgreSQL.

### III. Two Modes, Strictly Separated

qurator has exactly two generation modes, and they MUST NOT share a persistence path.

- **Ephemeral**: encode the supplied content, return the image bytes, store nothing.
  This path MUST NOT read from or write to the metadata store or blob store, and MUST
  NOT require an initialized driver of either.
- **Dynamic**: a short code persisted in the metadata store whose rendered image lives
  in the blob store, resolved at scan time to a target the owner can change later.
- The ephemeral handler MUST be allocation-conscious and MUST hold no lock contended by
  the dynamic path. A benchmark MUST cover it, and regressions in it are defects.
- Shared code between the modes is limited to pure encoding and rendering functions
  that take no store dependency.

*Rationale:* The fast path is a headline feature. Keeping it structurally incapable of
touching storage is the only way it stays fast as the dynamic side grows.

### IV. The Public Scan Path Is Sacred (NON-NEGOTIABLE)

Redirect and image-serving endpoints exist for strangers with phone cameras. They MUST
stay fast and MUST NOT be gated.

- `GET /r/{code}` and public image endpoints MUST be unauthenticated. Auth middleware
  MUST NOT be mounted on them.
- Scan analytics MUST be recorded asynchronously off the request path. A failing,
  slow, or saturated analytics writer MUST NOT delay or fail a redirect; it MUST drop
  events and increment a dropped-event metric instead.
- Image responses MUST carry correct `Cache-Control` and `ETag` headers, and MUST
  honour conditional requests.
- A redirect MUST require at most one metadata lookup, served from an in-process cache
  where possible.
- Redirect targets MUST be validated against an allowed-scheme list at write time to
  prevent the service becoming an open redirect into `javascript:` or `data:` URIs.

*Rationale:* A QR code is printed on physical objects and cannot be recalled. The scan
path's availability and latency are the product; everything else is administration.

### V. Unified JWT Auth With A Forward-Auth Escape Hatch

One credential model MUST serve both browsers and machines, and operators MUST be able
to delegate identity entirely to infrastructure they already run.

- Authentication MUST be JWT-based, accepted from either a `Authorization: Bearer`
  header or a secure, `HttpOnly`, `SameSite` cookie, so the web UI and API clients
  share one verification path.
- A **forward-auth mode** MUST be supported in which qurator trusts an identity header
  (`X-Forwarded-Email` by default) supplied by a proxy such as oauth2-proxy, Authelia,
  or Cloudflare Access. This is how OIDC and SSO are supported.
- Forward-auth mode MUST be explicitly enabled AND MUST require a configured list of
  trusted proxy sources. Trusting an identity header from an arbitrary client is a
  critical defect.
- qurator MUST NOT implement an identity provider, federation, or password reset flows
  of its own beyond a bootstrap local account.
- API tokens MUST be storable only as hashes, displayed exactly once at creation, and
  revocable without a restart.

*Rationale:* One verification path means one place to get auth right. Delegating SSO to
a proxy gives operators every IdP on the market for none of our maintenance burden.

### VI. Secure Defaults & 12-Factor Config

Configuration MUST come from the environment, and the default MUST be the safe choice.

- All configuration MUST be readable from environment variables, with an optional file
  as a convenience layer. Precedence MUST be flags > env > file > default.
- Anything that widens exposure — a publicly reachable ephemeral endpoint, permissive
  CORS, forward-auth trust, debug endpoints — MUST default to off and require explicit
  opt-in.
- The service MUST refuse to start with a missing or default signing secret when not
  in an explicitly flagged development mode.
- Secrets MUST NOT be logged, echoed to config dumps, or returned by any API.
- User-supplied encode payloads MUST be size-limited, and rendering MUST be bounded in
  dimensions and time to prevent resource-exhaustion via crafted requests.

*Rationale:* Self-hosted software is deployed by people who will not read every flag.
The default deployment must be the defensible one.

### VII. Test-First For Contracts (NON-NEGOTIABLE)

Contracts get tests before implementations.

- QR encoding and rendering, storage drivers, and HTTP endpoint contracts MUST have
  failing tests written and reviewed before the implementing code is written.
- Encoding correctness MUST be verified by decoding the rendered output and asserting
  round-trip equality — not by asserting on byte snapshots alone.
- Every storage driver runs the shared contract suite (Principle II).
- Every bug fix MUST begin with a regression test that fails before the fix.
- Beyond contracts, tests are expected but the ordering is not mandated; internal
  refactors need not be preceded by new tests.

*Rationale:* Scoping strict TDD to contracts puts the discipline where interchangeable
implementations and external promises make it pay, without ceremonial tests on every
private helper.

### VIII. Operability Is Part Of Done

A feature is not complete until it can be run and diagnosed in production.

- Structured logging (`log/slog`) with a request ID propagated through context. No
  `fmt.Println` in the server.
- `/healthz` (liveness, no dependency checks) and `/readyz` (readiness, checks
  configured stores) MUST both exist and MUST be distinct.
- Prometheus metrics MUST cover request rate, latency, and error rate per route, plus
  domain counters for generation, scans, and dropped analytics events.
- Container images MUST be reproducible, multi-architecture (amd64, arm64), run as a
  non-root user, and be published alongside every release.
- Graceful shutdown MUST drain in-flight requests and flush the analytics buffer.

*Rationale:* Observability retrofitted after an incident is observability that was
missing during the incident.

## Technology & Architecture Constraints

- **Language**: Go. The project MUST support the current and previous stable Go
  releases.
- **Dependencies**: The standard library is the default choice. Each third-party
  dependency MUST be justified in the plan that introduces it, and MUST be actively
  maintained and permissively licensed (MIT/BSD/Apache-2.0).
- **Layout**: `cmd/` for binaries, `internal/` for non-importable implementation,
  `pkg/` only for packages deliberately offered to external importers. Domain logic
  MUST NOT import HTTP or SQL packages.
- **Licensing**: The project is open source. Any dependency incompatible with that
  MUST NOT be added.
- **API stability**: HTTP endpoints are versioned under `/v1`. Breaking an endpoint's
  contract within a major version is prohibited; deprecate, then remove at the next
  major.
- **Data ownership**: Operators own their data. A documented, scriptable export path
  for all metadata MUST exist. No telemetry leaves the instance — no analytics
  callbacks, no version pings — unless explicitly enabled by the operator.

## Development Workflow & Quality Gates

- Work follows the Spec Kit flow: constitution → specify → clarify → plan → tasks →
  implement. Features begin as a specification, not as a branch of code.
- Every plan MUST pass the Constitution Check gate before task generation. A principle
  violation MUST be either removed or recorded in the plan's Complexity Tracking table
  with a justification and the simpler alternative that was rejected and why.
- CI MUST pass before merge: `go build`, `go vet`, `go test -race ./...`,
  `gofmt` cleanliness, and a linter (`golangci-lint`).
- Storage contract tests MUST run against every driver in CI, using ephemeral
  containers for PostgreSQL and an S3-compatible server.
- Changes to the public HTTP contract MUST update the API documentation in the same
  change that alters the behaviour.
- Performance-sensitive paths named in Principle III and IV MUST have benchmarks, and
  CI MUST surface regressions in them.

## Governance

This constitution supersedes other practices and conventions in this repository. Where
a linter, a habit, or a reviewer's preference conflicts with a principle here, this
document wins.

- **Amendments** MUST be proposed as a change to this file, stating the principle
  affected, the rationale, and the migration path for code that the change puts out of
  compliance. Amendments take effect on merge.
- **Versioning** follows semantic versioning:
  - **MAJOR**: a principle is removed, or redefined in a way that invalidates
    previously compliant code.
  - **MINOR**: a principle or a governed section is added, or existing guidance is
    materially expanded.
  - **PATCH**: clarification, rewording, or typo correction that does not change what
    is required.
- **Compliance review**: every pull request MUST be reviewed against these principles.
  Reviewers MUST cite the principle number when requesting a change on constitutional
  grounds. Complexity that serves no principle MUST be justified or removed.
- **Runtime guidance**: agent- and contributor-facing operational detail belongs in
  `CLAUDE.md` and `README.md`, which MUST NOT contradict this document. When they
  drift, this document is correct and they are the defect.

**Version**: 1.0.1 | **Ratified**: 2026-09-04 | **Last Amended**: 2026-09-04
