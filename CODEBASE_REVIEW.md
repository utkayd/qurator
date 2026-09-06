# qurator: codebase review and development plan

Reviewed 2026-09-06 against the current working tree. This is a local issue register, not a set of published GitHub issues. The initial review made no application changes; subsequent authorized implementation is recorded in the milestone status below.

The strongest direction is a dependable, self-hosted tool for creating **print-ready QR codes and managing their destinations over time**. Keep the single binary, offline console, SQLite default, optional PostgreSQL/S3, and anonymous analytics. The main constraint today is completing the user journey and making operational promises true end to end, rather than replacing the architecture.

## Architecture assessment

| Area | Assessment |
| --- | --- |
| Composition | `cmd/qurator` wires config, persistence, authentication, rendering, services, analytics, HTTP, and the embedded console. The dependencies are explicit and understandable. |
| QR engine | `internal/qr` separates encoding, geometry, PNG/SVG output, bounds, contrast, and logo policy. Independent decode tests are a strong correctness baseline. |
| Code management | `internal/codes` owns validation, images, batch creation, and redirect resolution. Its service is large but cohesive; split preparation, batch coordination, and resolution when changing those areas, without a wholesale rewrite. |
| Persistence | Store/blob interfaces and shared driver contracts provide a useful upgrade path. Metadata transactions cannot cover object writes, so cancellation and repair deserve more attention. |
| Scan path | Public routes have no authentication middleware; redirects use 302/no-store; analytics recording is asynchronous. Preserve these constraints. |
| Console | Embedded templates, HTMX, and vanilla JS suit the deployment model. Browser lifecycle behavior is the weakest-tested boundary. |
| Quality infrastructure | Unit, contract, architecture, integration, and HTTP console tests exist, alongside backend CI and benchmark comparisons. The directory named `tests/e2e` uses fakes and no JavaScript engine; it is not a real browser/system test. |

## Spec Kit basis and traceability

This revision incorporates the [constitution](.specify/memory/constitution.md), all three specifications and their implementation plans: [001 baseline](specs/001-qr-service-baseline/spec.md), [002 direct codes](specs/002-direct-codes/spec.md), and [003 storage URLs/batch](specs/003-storage-urls-batch/spec.md). Supporting evidence includes baseline research, tasks, data model, storage contract, OpenAPI excerpts, quickstart, and recorded quickstart results. Later specs extend the baseline: batch generation is no longer excluded merely because baseline assumptions originally deferred it. The Draft labels on 002/003 conflict with their completed task lists and present implementation; this review treats their requirements as the intended behavior while flagging status drift.

| Finding | Governing requirement or decision | Classification |
| --- | --- | --- |
| QUR-001 | 001 FR-006, FR-032, SC-001/002; T066 | Onboarding/documentation gap and tension in acceptance wording; config-only bootstrap itself is intentional. |
| QUR-002 | 001 FR-007, SC-001/008; 002 FR-102 | Scannability defect; define external-origin setup without widening anonymous access. |
| QUR-003 | 001 T113; Principle VI | Security coverage gap across two password entry points; T113 explicitly covers only the API route. |
| QUR-004–006 | 001 FR-033, FR-042, US6, SC-009; T090–093 | Console lifecycle defects; real-browser tests extend the prescribed HTTP test approach. |
| QUR-007 | 001 FR-002 is scoped to ephemeral generation; 002 SC-101 explicitly names downloaded PNG and SVG | Direct-code acceptance gap and misleading console control. PNG-only persistence is an existing implementation choice, not proof SVG download requirements were withdrawn. |
| QUR-008 | 003 FR-202/204/208; 001 FR-043 and research §5 CSP | Cross-feature UX gap. 003 specifically requires the storage link, but does not explicitly amend offline console/CSP guarantees. |
| QUR-009 | 001 FR-055, SC-013, FR-032/039; T095/097 | Documentation/scope gap; full artifact backup is a proposed extension, and password reset is prohibited in current scope. |
| QUR-010 | 001 SC-013; T095/097 | Restore robustness gap; staging/atomic import is a proposed design to protect the round-trip outcome, not an existing explicit atomic-import requirement. |
| QUR-011 | 001 FR-017/SC-008; research §4 | Implementation falls short of research's immediate local invalidation promise, but the described race remains within the formal 60-second bound. |
| QUR-012 | 003 FR-207; Principle II | Failure-cleanup hardening; do not confuse atomic metadata with atomic blob storage. |

Preserve immutable modes and images: direct-code destination/enable/disable requests remain refused (002 FR-104), direct analytics remain `not_tracked` (FR-105), and restyling means recreation (FR-109 and baseline assumptions). A manually requested direct short link still redirects and records an event by explicit 002 design; it is not a tracking defect. Preserve synchronous capped batches (003 assumptions), per-item render errors, one metadata transaction, and HTTP 200 results (FR-205/207). The story's “207-style” wording does not require HTTP 207.

Before feature implementation, use the repository's spec → clarify → plan → tasks sequence, including a Constitution Check. Begin fixes with failing regression tests (Principle VII), update affected contracts in the same change, and verify both driver contracts. This review proposes work; it does not amend approved product behavior.

## Issue register

The findings below describe the initial review; completed fixes are recorded in the milestone implementation status. P1 means address before promoting broader production adoption; P2 means prioritize during the next product iteration. Initial findings were based on source inspection; subsequent browser validation is recorded in feature 004. Live PostgreSQL/S3 were not exercised in this review.

### QUR-001 — P2: first-run guidance omits the required bootstrap configuration

**Evidence:** `internal/config/config.go:202` defaults bootstrap credentials to empty; `cmd/qurator/main.go:160` calls bootstrap only when an email is configured. No setup route exists in `internal/console/handler.go:51`. The warning inside `internal/auth/bootstrap.go:25` is therefore skipped in the default startup path. The README promises useful zero-configuration operation but supplies no initial sign-in instructions.

**Impact:** a fresh instance serves healthy endpoints and a sign-in page, but nobody can sign in. Existing zero-config tests principally prove startup and secret persistence.

**Action / acceptance:** first document the existing configured bootstrap values and give an actionable empty-user diagnostic without logging credentials. Test fresh data directory → configured bootstrap → sign in → create → download → decode, with no external service. FR-032 intentionally requires configured bootstrap values; no public registration or automatic account creation is missing. Reconcile SC-001's one-command/no-config-file promise and US1's zero-config example with FR-006's authentication requirement. A setup command would need a new scoped spec; it is not assumed part of the fix.

### QUR-002 — P1: dynamic codes can encode unusable relative scan addresses

**Evidence:** `internal/config/config.go:183` defaults `server.base_url` to empty; `internal/codes/service.go` builds `ScanURL` as `baseRaw + "/r/" + shortCode`, then encodes it in `materialise`. `internal/config/validate.go` validates the image public base but not the server base URL.

**Impact:** configuring only bootstrap credentials permits creating a dynamic image containing `/r/<code>`. A standalone phone scan has no website origin against which to resolve it. Malformed configured base URLs can also reach the encoder.

**Action / acceptance:** require a validated external HTTP(S) origin before dynamic creation, with guided configuration and clear explanation of domain permanence. Keep direct/ephemeral generation usable without an external origin. Never silently derive a permanent printed address from an untrusted Host header. Decode a generated dynamic PNG and assert an absolute, configured scan URL; reject unsupported base URL components and explicitly define subpath support.

### QUR-003 — P1: console sign-in bypasses the password rate limiter

**Evidence:** `internal/httpapi/router.go` applies `SigninLimiter` only to `POST /v1/auth/signin`. `POST /ui/signin` invokes `consoleAuth.SignIn` through `internal/console/handler.go:193` and `cmd/qurator/console_adapters.go:192`, including Argon2 verification, with no equivalent limiter.

**Impact:** the browser endpoint remains available for repeated password guesses and expensive password verification despite API protection.

**Action / acceptance:** share sign-in throttling and verification policy across both entry points. Test through the production router that both paths reject excess attempts before password verification. Preserve trusted-peer semantics and explain the shared limit behind a proxy.

### QUR-004 — P1: console error responses are invisible to HTMX users

**Evidence:** handlers return rendered validation errors with 400 and edit conflicts with 409. The vendored `htmx-2.0.4.min.js` defaults to `swap:false` for 4xx/5xx. `internal/console/assets/app.js` does not override response handling or handle `htmx:beforeSwap`/response errors.

**Impact:** invalid creation, invalid token input, or a concurrent edit can appear to do nothing even though the server renders a helpful error.

**Action / acceptance:** explicitly render expected validation/conflict responses into a stable error region and provide a separate unexpected-failure state. Add real browser tests for invalid destination, low contrast, and edit conflict; assert visible, accessible messages and preservation of input.

### QUR-005 — P1: controls are not initialized after HTMX body swaps

**Evidence:** `internal/console/assets/app.js:341` initializes controls only on `DOMContentLoaded`; script execution in swapped content is disabled. Token creation returns a 201 page containing the copy button into a form targeting `body` with `outerHTML`. No swap/load hook initializes that new button.

**Impact:** the successful token-creation workflow can display a copy button without a listener. Other controls bound directly to newly inserted elements have the same initialization gap.

**Action / acceptance:** use stable event delegation for global behavior and idempotent initialization on HTMX load/swap. Test create token → copy → navigate → revoke in a browser using the real binary, checking no duplicate listeners or lost CSRF headers.

### QUR-006 — P2: failed clipboard writes are reported as success and hide the secret

**Evidence:** `copyToClipboard` in `internal/console/assets/app.js` calls `then(done, done)` and also calls `done` when the Clipboard API is unavailable. The token callback removes the only visible secret and displays a success toast.

**Impact:** clipboard denial or an unsupported context can cause users to lose access to a newly generated token before recording it.

**Action / acceptance:** only hide after confirmed successful copying; on failure retain selectable text and explain manual copying. Test rejection and missing Clipboard API, in addition to success.

### QUR-007 — P2: the format selector promises an SVG that is never saved

**Evidence:** `internal/console/templates/code_new.html` offers PNG/SVG; `internal/console/handler.go:277` reads `Format` but does not pass it into creation. `codes.materialise` persists PNG and the public image handler accepts only `.png`.

**Impact:** selecting SVG affects the preview but the saved/downloadable artifact remains PNG. Dynamic preview also encodes the destination rather than the eventual scan URL, so its density differs from the printed output; the template acknowledges this but the JS commentary claims exact matching.

**Action / acceptance:** correct the misleading saved-format control as an interim fix, then fulfill 002 SC-101 with true SVG downloads generated from the stored code's canonical payload and effective styling. Removing the selector alone does not fulfill that success criterion. Clearly distinguish an approximate draft from the final image. Independently decode downloaded PNG and rasterized SVG and compare their payloads, covering direct mode and the styling matrix.

### QUR-008 — P2: console images ignore configured image URL mode

**Evidence:** `internal/console/handler.go:403` hardcodes `/i/<id>.png`; `internal/httpapi/public/image.go` returns 404 when `images.serve_via_instance=false`. The service already has `ImageURL`, but the console only obtains `StorageURL` as a supplementary field.

**Impact:** a supported S3-only image deployment breaks the main console preview/download path.

**Action / acceptance:** use the configured image URL for explicit download/open actions and keep the required storage link. Define how the console presents a preview when instance serving is disabled; a clear unavailable-preview state with a working download is preferable to a broken image. Do not silently relax research §5's self/data-only image CSP or FR-043's offline promise. External embedded previews require an explicit spec/CSP policy decision. Test instance, public, and presigned modes, including expired links.

### QUR-009 — P2: metadata export documentation overstates recovery scope

**Evidence:** `internal/export/write.go` exports metadata only and has no blob-store dependency. Imported codes retain image/logo keys, but `internal/export/read.go` neither restores nor regenerates images. Imported local users have no password, and `auth.Bootstrap` refuses to act once users exist. README language promises walking away with everything and mentions password reset without providing a recovery command.

**Impact:** importing into fresh storage leaves image endpoints without their artifacts and local users without a supported sign-in recovery path. Preserving the old scan domain is also necessary for already-printed dynamic images.

**Action / acceptance:** document portable metadata export separately from complete operational backup; remove instructions implying that a password reset command exists. Provide a tested backup procedure for database, images/logos, and signing key, with guidance to retain the printed scan domain. Metadata-only export is consistent with FR-055; do not call absent blobs a violation of that requirement. A unified complete-backup command needs a new specification. Password recovery/reset requires changing FR-039 and Constitution Principle V before implementation, rather than inferring an exception for a CLI. Verify SC-013's metadata equality independently from a full operational restore drill.

### QUR-010 — P1: failed imports leave partial state and incomplete archives can succeed

**Evidence:** `internal/export/read.go:26` writes rows as archive entries arrive, requires a manifest only by end of input, and does not compare entity counts against the manifest. There is no import-wide transaction or staging store. A manifest-only archive is accepted; a later malformed row can leave earlier users inserted and make the next ordinary import fail the nonempty-store check.

**Action / acceptance:** preflight version, required entries, counts, references, and checksums; import through staging or transactional primitives with a defined commit boundary. Test missing entries, truncation, duplicate records, and backend failure. A rejected import must leave the target unchanged, and a retry must work without manual database cleanup. Also cover alias release/reuse histories rather than only isolated reservations.

### QUR-011 — P2: cache invalidation can lose a race to an in-flight lookup

**Evidence:** `internal/codes/service.go:927` performs cache Get → store read → cache Set independently of mutation/invalidation. A lookup can read the old destination, an update can commit and invalidate, and that lookup can subsequently repopulate the cache with the old value.

**Impact:** even on one instance, the next scan is not guaranteed to see the update, contrary to the invalidation comment. The 30-second TTL bounds this and remains within the documented 60-second propagation budget, so this is not an unbounded stale-redirect defect.

**Action / acceptance:** choose and document the consistency promise. If immediate local visibility is intended, use generation-aware fills or coordinated per-key mutation/loading. Use a deterministic blocking-store test for destination, disable, and delete races. Define multi-instance propagation separately.

### QUR-012 — P2: canceled creates can leave orphaned blobs

**Evidence:** `internal/codes/service.go:557` performs compensating deletes with the same request context used for writes. When that context has been canceled, a context-respecting backend can reject cleanup as well. Failures are logged but no reconciliation tool exists.

**Action / acceptance:** give compensation a short independently bounded cleanup context; add an operator reconciliation command with a dry-run inventory and age threshold. Test cancellation after a successful object write but before metadata commit against a backend fake that honors cancellation. Do not remove retained images of soft-deleted codes.

## Prioritized delivery plan

The phases are dependency order, not calendar commitments. Estimates should follow scoped specs and the regression tests for each issue.

| Phase | Scope | Exit criteria |
| --- | --- | --- |
| 1 — A trustworthy first code | QUR-001–005; fix clipboard failure; correct misleading format control; real-browser CI smoke flow | Fresh install → documented configured bootstrap → sign in → create → download → decode → change destination → scan succeeds. Bad input is visible, tokens can be copied/revoked, both sign-in routes are throttled. |
| 2 — Safe operation and recovery | QUR-008–012; tested full-state backup procedure; metadata import hardening; configuration diagnostics; documented domain ownership and migration | Full-state backup restore recovers working images, redirects, analytics, and administrative access without adding a password reset flow. Bad metadata archives leave no partial state. S3 download links work in console under the explicit CSP policy. Consistency and cleanup behavior have deterministic tests. |
| 3 — A complete creation workspace | Genuine PNG/SVG downloads, logo controls using the existing engine, reusable styling presets, names/tags/search, duplicate-code action, enable/disable UI | A nontechnical user can create, organize, find, pause, and download a campaign without API calls. Downloaded artifacts round-trip through the decoder; mobile and keyboard flows pass browser tests. |
| 4 — Repeated and bulk work | CSV import with column mapping, validation preview, progress/results, retry using existing `client_ref`, ZIP image download plus manifest | Mixed-validity imports preserve item identity and clearly report failures. Repeated submission does not duplicate successful items. Load tests show bulk work does not starve redirects. |
| 5 — Useful feedback and release polish | Date-range analytics, bot filtering controls, explicit delayed/unavailable states, CSV analytics export; versioned API examples; distributable binaries/checksums; release gates | Users can explain counts and distinguish zero scans from analytics failure. A new operator can install a release and complete the Phase 1 flow using README instructions alone. |

Phase 3 mixes completing an existing criterion (002 SC-101 SVG downloads) with proposed UX extensions (presets, tags, duplication, more console controls). The baseline explicitly permits advanced features to remain API-only; absence of every API control from the console is not automatically a defect. Phase 4 adds a console workflow over the already-specified batch API; keep synchronous bounded chunks and do not introduce async jobs without a new demonstrated need and spec. Phase 5 date-range/bot controls expose existing analytics capabilities, rather than inventing a new analytics model; bots remain recorded and filterable, never dropped.

Phase 3 should prioritize URL creation and print/export quality. Wi-Fi, contact, email, and text payload builders can initially use ephemeral generation, which already accepts arbitrary content. Persisted non-URL direct payloads would change 002 FR-103 and need a new specification rather than weakening shared URL validation. Keep arbitrary payloads away from dynamic redirect targets.

Avoid expanding into team permissions, custom-domain automation, or sophisticated campaign routing until the single-operator workflow and restore story are dependable. These introduce authorization, certificate/domain lifecycle, and scan-path complexity. Do not add mandatory cloud services or geographic tracking to solve unrelated UX gaps.

## Engineering work supporting the roadmap

- Add a small real-browser suite against the compiled application with SQLite/filesystem storage. Cover HTMX errors and swaps, token clipboard rejection, actual downloads, and mobile/keyboard interaction. Keep the existing HTTP tests; they provide useful fast coverage.
- Continue using the shared backend contracts. Add failure-injection cases at transaction/blob boundaries and real cross-backend restore tests. Do not interpret packages with no local test files as untested when contract suites exercise them elsewhere.
- Put process-wide admission limits around expensive rendering if concurrent load testing shows contention; per-batch workers currently bound one request, not aggregate demand across requests. Measure redirect latency while several large batches render, alongside cancellation latency and peak memory.
- Remove obsolete stream/worktree instructions from production comments such as the header of `cmd/qurator/export_cmd.go`. Keep implementation comments about invariants and tradeoffs.
- Make API-contract checks validate schemas and semantics as well as route presence. Retain the existing batch/direct/storage URL OpenAPI additions; these are already documented.
- Release workflow currently publishes containers before smoke tests and does not build/upload standalone release binaries. Build candidate artifacts, smoke-test readiness and core behavior, then promote release tags; pin tool/container inputs where reproducibility is promised. Choose and add a project license: only the vendored HTMX license is present in tracked files. License selection belongs to the maintainer.
- Evaluate first-code completion time, independently decoded artifact success, redirect latency under mixed load, successful restore drills, and dropped analytics events. Collect these in tests or locally; preserve the no-outbound-telemetry default.

## Validation and limits

## Milestone implementation status

The first reliability milestone is implemented in feature directory `specs/004-first-code-reliability`.

Docker packaging is also complete in `specs/005-docker-packaging`: fixed the final
image digest scope and fresh-volume ownership, built ARM64 and AMD64 images, and
verified health, authenticated QR generation and persistence across container
replacement on both. Pull-request and release workflows now share these checks.

- Dynamic creation now requires a validated configured scan origin; direct and ephemeral generation remain available without one.
- API and console sign-in share one TCP-peer rate-limit bucket.
- HTMX expected errors and unexpected/network failures are surfaced safely while preserving forms.
- Console controls reinitialize after swaps, optimistic edit versions advance from response ETags, and clipboard failure never hides a token.
- Four real-browser tests run against the compiled binary; the full Go, race, vet, lint, format, diff, and static-build checks pass.

The existing [quickstart results](specs/001-qr-service-baseline/checklists/quickstart-results.md) record a prior live PostgreSQL/MinIO run, redirect p99 around 0.42 ms with a stalled writer, and 512px PNG rendering around 3.1 ms. These are historical repository evidence, not fresh measurements from this review. Future acceptance should retain the actual targets: 001 SC-003/004 (<50 ms p99 at 1,000 scans/s), SC-006 (<20 ms typical generation and 500/s), SC-008 (updates within 60s), SC-013 (metadata equality), and 003 SC-202 (300-item batch under 5s on defaults, retry creates zero rows). Do not replace those gates with vague “fast enough” checks.

Artifacts also need a consistency pass: baseline US3 scenario 7 and portions of plan/tasks still say missing signing secrets must fail, while current FR-040 and the recorded deviation specify generated persistent secrets. T090 intentionally prescribes HTTP/HTML tests, so their implementation is plan-compliant even though they miss JS bugs. T101 explicitly prescribes push-then-smoke-test, so pre-publication promotion gates are a proposed improvement, not failure to implement that task. The data model prohibits passwordless local users, while import deliberately creates them; resolve that contract conflict without inventing a forbidden reset flow. Reconcile 002 SC-101's two downloaded formats with its PNG-focused T201 and current image endpoint. Update these artifacts explicitly instead of treating checked task boxes as proof that every success criterion was exercised.

Both `go test ./...` and `go test -race ./...` passed during this review. No live PostgreSQL/S3 endpoints were configured for this review; their suites are conditional, and local passing output is not evidence of live backend coverage. The initial browser findings were traced against application JS and the vendored HTMX response policy; subsequent implementation passed four real-browser tests, as recorded in feature 004. This is not an exhaustive security audit, dependency vulnerability audit, or measured production load assessment.
