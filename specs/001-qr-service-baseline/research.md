# Phase 0 Research: qurator v1

**Feature**: [spec.md](./spec.md) | **Date**: 2026-09-04 | **Plan**: [plan.md](./plan.md)

Six parallel research streams investigated the candidate stack. Every API claim below was
verified against real module source via `go doc -all`, a compiling spike, or a benchmark —
not recalled. Four candidate libraries from the initial stack did not survive that
verification. Those reversals are recorded in full, because the reasons generalise.

---

## Summary of stack reversals

| Area | Initial candidate | Outcome | Reason |
|------|------------------|---------|--------|
| Auth | `go-pkgz/auth/v2` | **REJECTED** | No `Authorization: Bearer` support at all; zero revocation; heavy transitive deps |
| Router | `go-chi/chi/v5` | **REJECTED** | Stdlib `ServeMux` now exposes the route pattern; chi adds nothing we need |
| QR render | `yeqown/go-qrcode/v2` | **REJECTED** | Its SVG writer does not exist; PNG-only |
| QR render | — | **NEW** | `piglig/go-qr` chosen; module shapes must be built in-house |
| Migrations | goose `.sql` set | **REVISED** | Cross-dialect `.sql` proven impossible; Go-based migrations instead |

---

## 1. QR encoding, rendering and styling

### Decision: `github.com/piglig/go-qr` v1.1.0 as the encoder and SVG/PNG source

- **Rationale**: MIT, actively maintained, and the only candidate providing PNG *and* SVG
  output plus logo overlay with an ECC-budget check. Verified deterministic: two renders of
  identical input produced byte-identical output, which FR-004 requires for HTTP caching.
- **Alternatives considered**: `yeqown/go-qrcode/v2` — rejected, see below.
  `skip2/go-qrcode` — no SVG, no styling, effectively unmaintained.
  `boombuler/barcode` — general barcode library, no QR styling.
- **Verified how**: throwaway module, `go doc -all`, and a spike that encoded → rendered
  PNG and SVG → decoded with an independent decoder → compared.

### Decision: `yeqown/go-qrcode/v2` is REJECTED — its SVG writer does not exist

- **Rationale**: The plan assumed a separate SVG writer module. The agent checked every
  subpackage and all 45 repositories in the `yeqown` GitHub organisation. There is no SVG
  writer. The library is PNG-only. FR-002 requires vector output, so this is disqualifying.
- **Lesson recorded**: this candidate was proposed from familiarity with the library's
  reputation for styling, and the SVG writer was assumed rather than checked. It is the
  clearest argument for the verify-don't-recall rule applied throughout this research.

### Decision: module shape rendering is BUILT IN-HOUSE

- **Rationale**: No Go library combines SVG output with dot/rounded module shapes.
  `piglig/go-qr` has SVG but no shape customisation; `yeqown`'s standard writer has shape
  hooks but is PNG-only. FR-026 requires shapes and FR-002 requires both formats, so the
  intersection does not exist off the shelf.
- **Design**: one shared shape function reads `piglig`'s `Module(x, y) bool` grid and emits
  geometry consumed by both a PNG rasteriser and an SVG string builder. Square, circle, and
  rounded-rect (via neighbour adjacency, so runs of modules join smoothly).
- **Consequence**: this is real, sized work, not a library call. It carries a compensating
  benefit — because both formats derive from one geometry function, PNG and SVG output
  cannot drift apart, which a two-library approach could not guarantee.
- **Alternatives considered**: PNG-only shapes with plain-square SVG — rejected, it makes
  output format silently change appearance, a real defect for a branding feature.

### Decision: `github.com/makiuchi-d/gozxing` v0.1.1 as the INDEPENDENT test decoder

- **Rationale**: Principle VII requires decode-based round-trip verification, not byte
  snapshots. The decoder must be independent of the encoder or the test proves nothing.
- **Critical detail**: binary payloads must be read via
  `ResultMetadataType_BYTE_SEGMENTS`, **not** `.GetText()`, which mangles non-UTF8 bytes.
  A test using `GetText()` would pass on ASCII and silently fail the binary round-trip
  FR requires.
- **SVG verification**: SVG output is rasterised with `srwiley/oksvg` + `rasterx` (pure Go,
  no cgo) before decoding, so vector output is verified as genuinely scannable rather than
  assumed correct.
- **Verified how**: end-to-end spike covering Unicode, emoji, RTL text, and raw bytes.
  License confirmed MIT by reading the LICENSE file directly (GitHub reports NOASSERTION).

### Decision: `boostEcl` MUST be disabled

- **Rationale**: `piglig/go-qr`'s convenience encoders hardcode `boostEcl=true`, silently
  promoting the requested error-correction level — a request for `Low` returns `Medium`.
  FR-026 lets the user choose the EC level, and silently overriding it makes FR-027's
  automatic raising indistinguishable from library behaviour.
- **Resolution**: call `EncodeSegments(..., boostEcl=false)` directly and perform all EC
  adjustment in qurator's own policy layer, where it is explicit and testable.
- **Verified how**: spike observed a `Low` request returning a `Medium` symbol.

### Decision: concrete styling limits

| Limit | Value | Source |
|-------|-------|--------|
| Logo area, EC-L | 5% of module area | `piglig` `eccRecoveryBudget()`, below ISO 7% for margin |
| Logo area, EC-M | 12% | ISO 15% |
| Logo area, EC-Q | 20% | ISO 25% |
| Logo area, EC-H | 25% | ISO 30% |
| Contrast hard floor | 3:1 | WCAG relative luminance |
| Contrast default gate | 4.5:1 | Recommended default |
| Max payload (FR-005) | 2,953 bytes at EC-L → 1,273 at EC-H | ISO/IEC 18004 capacity table |

- **Contrast rationale**: gozxing's binarisers (`HybridBinarizer`,
  `GlobalHistogramBinarizer`) threshold on luminance — the same basis as the WCAG relative
  luminance formula. So a WCAG-style ratio is not an arbitrary borrowing from accessibility;
  it measures the quantity real scanners actually discriminate on.

### Decision: policy layer is ours, not the library's

No candidate library auto-raises EC for a logo (FR-027), enforces contrast (FR-028), or
bounds output dimensions and render duration (FR-029). All four are qurator's own code.

- **Performance**: ~2.5ms per render single-threaded (~400/sec/core), no shared mutable
  state found. SC-006's <20ms and 500/sec targets have comfortable headroom across cores.

---

## 2. Authentication

### Decision: `go-pkgz/auth/v2` is REJECTED; build ~280 lines on `golang-jwt/jwt/v5`

- **Rationale**: three disqualifying findings, all read from source:
  1. Its unified `token.Service.Get` reads a token from a query parameter, a custom `X-JWT`
     header, or its own cookie. There is **no `Authorization: Bearer` path at all** —
     directly failing FR-031.
  2. **Zero revocation support** anywhere in the module tree (grepped) — failing FR-035.
  3. It hard-fails unless `len(claims.Audience) == 1`.
  4. `go get` pulled a Mongo driver, bbolt, OAuth1/OAuth2 stacks, SCRAM auth, and an
     identicon library — heavy weight for one feature.
- **What we give up**: OAuth providers (irrelevant — forward-auth covers SSO by design),
  avatars, and RBAC helpers (replaced by an `IsAdmin` bool).
- **Alternatives considered**: `golang-jwt/jwt/v5` verified to have a genuinely
  zero-dependency `go.mod`. Combined with stdlib `net/http` and `crypto`, ~280 lines
  covers every auth requirement in the spec with no unused surface.
- **Note on the brief**: the project owner asked for an out-of-the-box package *if one
  exists*, with an explicit fallback otherwise. Verification established the condition is
  false, so the fallback applies.

### Decision: API tokens are hashed with SHA-256, NOT Argon2id

- **Rationale**: this inverts the usual instinct and is worth stating precisely. Argon2id
  exists to make *low-entropy, human-chosen* secrets expensive to guess. An API token is
  32 bytes from a CSPRNG — 256 bits — so it is already beyond brute force. A slow KDF
  therefore buys **zero** additional resistance while adding real latency to every
  authenticated API call and handing an attacker a CPU-exhaustion vector: spraying invalid
  tokens would force expensive hashes on the hot verification path.
- **Format**: `qur_<32 random bytes, base64url>`. Compared with
  `crypto/subtle.ConstantTimeCompare`.
- **Optional hardening**: HMAC-SHA256 with a server-side pepper, so a stolen database
  alone does not permit offline verification.
- **Alternatives considered**: Argon2id and bcrypt — rejected as above.

### Decision: the ONE human password uses Argon2id

- **Parameters**: `golang.org/x/crypto/argon2.IDKey`, RFC 9106 §4 lighter profile —
  time=3, memory=64 MiB, threads=4, keyLen=32, 16-byte random salt, PHC-encoded.
- **Rationale**: the admin password IS human-chosen and low-entropy, so the reasoning above
  reverses entirely. The distinction between the two credential types is the whole point.
- **Note**: `x/crypto/argon2` exposes only raw `IDKey`/`Key` with no bcrypt-style wrapper,
  so ~30 lines of PHC encoding/verification is ours.

### Decision: revocation via short-TTL positive cache + per-user token version

- **Design**: the database is the source of truth. A 30-second *positive* cache (not a
  blacklist) keyed by token ID fronts API-token lookups. Session JWTs carry a per-user
  `token_version` claim checked against a column, so bumping the column invalidates every
  session at once.
- **Worst-case propagation**: 30 seconds — half the 60-second budget FR/SC-011 allows.
- **Alternatives considered**: a revocation blacklist (unbounded growth, and fails open if
  an entry is missed); per-request DB lookup (correct but puts a query on every API call).

### Decision: forward-auth trust is decided by the TCP peer, never by header content

- **Rule**: read the immediate peer via `net.SplitHostPort(r.RemoteAddr)`, test that IP
  against an operator-configured CIDR allowlist (`net.ParseCIDR`,
  `(*net.IPNet).Contains`), and only then read the identity header **directly**.
- **Why not `X-Forwarded-For`**: deciding trust from XFF is self-referential — the
  attacker controls the very header that would authorise them. This is the single most
  common forward-auth vulnerability.
- **Conflicting assertions**: if the identity header appears more than once, or is
  comma-joined into multiple values, **refuse the request**. Never pick one. (FR-038)
- **Fail closed**: forward-auth enabled with an empty trust list must refuse to start.
  That combination is precisely the misconfiguration that turns the header into an open
  login for anyone who can reach the port.

### Decision: cookie attributes

`HttpOnly; Secure; SameSite=Strict; Path=/`, no `Domain` (host-only, so the cookie is
instance-scoped per FR-031).

- **Rationale**: `SameSite=Strict` does not cover every same-site edge case and does not
  apply to Bearer auth at all — CSRF is a cookie-only problem. A required custom header on
  mutating requests provides the second layer. See §5 for the console-side mechanics.

---

## 3. Persistence

### Decision: `database/sql` with hand-written per-dialect SQL

- **Rationale**: `database/sql` is the only layer common to `modernc.org/sqlite` and pgx.
  sqlx, sqlc, and query builders were each evaluated and rejected: none of them actually
  removes the DDL, upsert, and identity divergence between the two backends — they add a
  dependency while leaving the real problem. Hand-written SQL per driver, behind one
  `Store` interface, keeps the divergence visible where it must be handled.
- **Verified how**: live queries against SQLite 3.53.4 and Postgres 16.

### Decision: `modernc.org/sqlite` v1.58.0 — confirmed pure Go

`CGO_ENABLED=0 go build` cross-built a genuinely statically linked Linux ELF, verified
with `file`. No libsqlite dependency on macOS either. This is what makes Principle I's
single static binary true.

### The complete dialect-divergence list

| Divergence | SQLite | PostgreSQL | Handling |
|-----------|--------|-----------|----------|
| Placeholders | `?` | `$1` | Separate SQL strings per driver; no shared templating |
| Upsert | `ON CONFLICT ... DO UPDATE` | identical | Verified identical in both — no branch needed |
| `RETURNING` | supported (≥3.35; bundled 3.53.4) | supported | No branch needed |
| Identity column | `INTEGER PRIMARY KEY AUTOINCREMENT` | `GENERATED ALWAYS AS IDENTITY` | **Irreducible — must branch in DDL** |
| Case-insensitive unique | `COLLATE NOCASE` | `UNIQUE INDEX ON (lower(code))` | **Must branch**; query via `lower()` on PG |
| Timestamps | UTC RFC3339 TEXT | `TIMESTAMPTZ` | Driver-level scan/format convention |
| Booleans | INTEGER 0/1 | `BOOLEAN` | Driver-level convention |

`CITEXT` was verified to work as a Postgres alternative but rejected: it requires
`CREATE EXTENSION`, which is a privilege a managed-database user may not have — an
avoidable barrier to self-hosting.

### Decision: goose **Go-based** migrations, not `.sql` — CONSTITUTIONAL AMENDMENT REQUIRED

- **Finding**: goose does **not** translate SQL across dialects; its `Dialect` setting
  governs only its own bookkeeping table. Proven by running a SQLite-flavoured
  `AUTOINCREMENT` migration against live Postgres: it failed with a genuine syntax error,
  SQLSTATE 42601.
- **Consequence**: Constitution Principle II's "applied for both SQL backends from the
  same migration set" is **not achievable** with a single `.sql` set for any realistic
  schema. This is a real conflict between the constitution and reality, not a preference.
- **Resolution**: goose's Go-based migrations (`func(*sql.Tx) error`) give one ordered
  version sequence whose DDL text branches per dialect at runtime. Verified working
  end-to-end against live SQLite and live Postgres. One source of version ordering is
  preserved — which is the property that actually prevents backend drift — while the DDL
  text is written twice per migration.
- **Action**: amend the constitution to v1.0.1, restating the requirement as *one ordered
  migration sequence*. PATCH bump: this clarifies what the principle was protecting, and
  does not weaken it.

### Decision: `minio-go/v7` for S3-compatible storage

- **Measured**: 5.72MB vs 7.39MB stripped binary, and 59 vs 113 transitive dependencies,
  against `aws-sdk-go-v2` for equivalent client construction.
- **Rationale**: purpose-built for S3-compatible endpoints (MinIO, Garage, R2, Backblaze),
  which is exactly the self-hosting audience. The AWS SDK carries STS and SSO machinery
  that is dead weight here.

### Decision: filesystem blob driver durability

Shard by hash prefix; write a temp file in the same directory → `fsync` the file →
`rename` → **`fsync` the parent directory**. That last step is the commonly missed one:
without it, the rename itself may not survive a crash, so a blob can appear written and
then vanish.

### Decision: contract tests skip on missing DSN, not testcontainers

- **Design**: one `TestStore(t, newStore)` / `TestBlobStore(t, newBlobStore)` function
  called from each backend's thin wrapper. Postgres and S3 subtests call `t.Skip()` when
  `QURATOR_TEST_PG_DSN` / `QURATOR_TEST_S3_ENDPOINT` are unset.
- **Rationale**: a contributor running `go test ./...` with no Docker gets a green,
  honest run with skips reported. `testcontainers-go` would make Docker a prerequisite for
  participating at all — a contributor-experience cost that contradicts the project's
  self-hosting, low-barrier ethos. CI supplies the env vars via service containers.

### Decision: sentinel errors and their driver detection

`ErrNotFound`, `ErrAliasTaken`, `ErrConflict`, `ErrBlobNotFound`.

- SQLite: extended result code `2067` (`SQLITE_CONSTRAINT_UNIQUE`) via `modernc.org/sqlite/lib`.
- Postgres: SQLSTATE `23505` via `pgconn.PgError`, extracted with plain `errors.As`.

Both verified with live constraint violations.

---

## 4. Scan path and analytics

### Decision: `github.com/maypok86/otter/v2` cache — 50,000 entries, 30s TTL

Benchmarked on a skewed 80/20 hot-key workload, which is qurator's actual pattern (one
campaign code taking most traffic):

| Cache | ns/op | Notes |
|-------|-------|-------|
| **otter/v2 (W-TinyLFU)** | **3.9** | Zero-alloc; chosen |
| `sync.Map` + manual TTL | 10.2 | No bounded memory without hand-rolling eviction |
| ristretto | 23.4 | |
| hashicorp `expirable.LRU` | 229 | `Get` takes a single global mutex — collapses under hot keys |

The hashicorp result is the instructive one: its `Get` acquires one global mutex, so the
*more* skewed the workload, the worse it performs — the exact opposite of what a cache
should do under a hot key. Confirmed by reading its source, not just the benchmark.

- **Invalidation**: delete-on-write for same-instance immediacy; the 30s TTL is a backstop
  covering what explicit invalidation cannot reach (restart, future multi-instance). That
  leaves 30s of the 60-second SC-008 budget as margin.

### Decision: async pipeline shape

Buffered channel (capacity 10,000) → 2–4 consumer goroutines → batch of 200 events or a
500ms ticker, whichever comes first.

- Producer uses `select` with `default:` to drop-and-count — genuinely non-blocking, so a
  saturated writer cannot touch redirect latency (Principle IV).
- Shutdown closes the channel and waits on a `sync.WaitGroup` under a caller-supplied
  `context` deadline, so drain is **bounded** and a stuck sink cannot hang shutdown.

### Decision: batched inserts, 200 per transaction

Benchmarked on `modernc.org/sqlite` (WAL, `synchronous=NORMAL`):

- Individual autocommit inserts: ~92,833 rows/sec, acquiring the single writer lock once
  per row.
- Batched at 200/transaction: ~341,316 rows/sec — 3.7× — acquiring the writer lock 200×
  less often.

The throughput number is not the real prize. SQLite has one writer; taking that lock 200×
less often is what stops analytics writes from starving foreground writes. Postgres `COPY`
is a documented escalation path, unnecessary at this batch size.

### Decision: `github.com/medama-io/go-useragent`

| Library | ns/op | Allocs | Status |
|---------|-------|--------|--------|
| **medama-io/go-useragent** | **314.6** | **0** | Chosen |
| mssola/user_agent | 731.9 | 13 | Stale since 2023 |
| ua-parser/uap-go | 1,225 | — | Regex-based |

- **Binary cost, independently re-verified**: the research initially reported a 15MB embed.
  That is the module *directory* size; its only `go:embed` directive is `data/final.txt` at
  152KB. Measured binary impact: 1.57MB baseline → 3.40MB, so **~1.8MB**. Recorded because
  a 15MB embed would have been worth reconsidering against Principle I, and a wrong number
  would have driven a wrong decision.
- **Fields**: "family" = browser name; "device category" = {Desktop, Mobile, Tablet, TV, Bot}.

### Decision: `302 Found` with explicit no-store headers

`Cache-Control: no-store, no-cache, must-revalidate` plus `Pragma: no-cache`.

- **Rationale**: 301 and 308 are heuristically cacheable indefinitely by clients. Using one
  would break FR-010/SC-008 — a destination change would never reach anyone who had already
  scanned — *and* make repeat scans invisible to analytics, silently, for exactly the most
  popular codes. 302's own semantics are not a strong enough guarantee on their own, so the
  explicit `no-store` header does the real work.

### Decision: rollups computed in the same transaction as the raw insert

Hourly `(code, hour_bucket)` totals plus `(code, hour_bucket, dimension, value)` rows,
upserted with `ON CONFLICT ... DO UPDATE count = count + ?` from the same in-memory batch
and the same transaction as the raw event insert.

- **Rationale**: this makes FR-023's "every dimension breakdown totals to the overall
  count" true **by construction** rather than by reconciliation. A separate periodic
  rollup job would introduce a window where the two disagree, and reconciliation logic
  that must be correct forever. Aggregates are retained indefinitely, decoupled from raw
  event retention (FR-024).

### Decision: chunked retention delete

`DELETE FROM scan_event WHERE id IN (SELECT id FROM scan_event WHERE ts < ? LIMIT 1000)`,
run daily.

- **Verified**: SQLite's `DELETE ... LIMIT` form is a syntax error without a special
  compile flag; the subquery form works and is portable to Postgres unchanged.

### Decision: bots are TAGGED, never dropped

Classified via the UA parser's `Bot` device category and recorded normally, filterable at
query time.

- **Rationale**: dropping bot events would break the sum-equals-total invariant above, and
  any maintained bot list rots. Tagging keeps the data honest and lets the operator decide.
- **Verified**: Googlebot, facebookexternalhit, Slackbot-LinkExpanding, and curl all
  classified correctly.

---

## 5. Web console

### Decision: NO JavaScript build step — Go `html/template` + htmx + one small vanilla JS file

- **Rationale**: the decisive factor is the auth model we already chose. With a JWT in an
  HttpOnly cookie, JavaScript *cannot read the token*, so an SPA needs an entire
  client-side auth-bootstrap layer to work around that. Server-rendered pages already have
  the server as the sole auth authority on every request — the problem disappears rather
  than being solved. Secondarily, htmx is one dependency-free ~14KB file, against an SPA's
  runtime plus bundler plus lockfile plus transitive tree, all of which Go maintainers
  would have to own.
- **Alternatives considered**: vanilla ES modules with no build (viable, but hand-rolls
  what htmx already does); a bundled SPA (rejected on the auth-model and maintenance
  grounds above).

### Decision: live preview calls the server's ephemeral endpoint, debounced 150–200ms

With `AbortController` cancellation of stale in-flight requests and a small client-side
memoisation cache keyed by style parameters.

- **Rationale**: the preview then uses *the exact renderer* that produces the final image,
  so preview fidelity is guaranteed. Client-side JS QR rendering would mean a second
  encoder implementation and risks precisely the preview/output mismatch that would be a
  serious defect in a branding feature.

### Decision: charts are server-rendered inline SVG

- **Rationale**: a single trend line does not justify uPlot (~25–50KB) or Chart.js
  (~45–120KB gzip), and the no-external-origin rule (FR-043) means any library must be
  vendored and maintained in-repo. uPlot is recorded as the fallback if requirements grow
  to multi-series or zoom.

### Decision: strict CSP with per-request nonces

```
default-src 'none'; script-src 'self' 'nonce-<per-request>';
style-src 'self' 'nonce-<per-request>'; img-src 'self' data:;
font-src 'self'; connect-src 'self'; form-action 'self';
frame-ancestors 'none'; base-uri 'none'
```

- **Constraint discovered**: htmx's bracketed-trigger syntax and dynamic style injection
  normally want `unsafe-eval`/`unsafe-inline`. Mitigated by restricting htmx to plain
  trigger events, banning `hx-on:*` inline handlers, and pre-declaring transition CSS
  rather than letting htmx synthesise inline styles. No unsafe directives are needed —
  but this is a real constraint on how the console may be written, so it belongs in
  contributor documentation, not just in the header.

### Decision: CSRF — mandatory custom header plus SameSite

A required `X-Qurator-Requested-With` header on every state-changing request, plus the
`SameSite=Strict` cookie, plus strict no-CORS on cookie-authenticated routes.

- **Rationale**: `SameSite` alone leaves gaps (state-changing safe methods, and the
  documented lax-POST grace window). A cross-site HTML form **cannot set a custom header**,
  which closes the classic vector. Bearer-token API clients are CSRF-immune by
  construction and unaffected.

---

## 6. Server foundation, configuration and operations

### Decision: stdlib `net/http.ServeMux`, NOT chi

- **Rationale**: the reason chi was in the stack was low-cardinality route labels for
  metrics. Verified that Go 1.22+ exposes the matched pattern via `r.Pattern`
  (`"GET /r/{code}"`), giving the same guarantee as `chi.RouteContext(ctx).RoutePattern()`.
  With that equalised, no needed feature is missing — auth-differentiated route groups are
  two sub-muxes with different middleware wraps — so the dependency does not earn its place.
- **Why this matters**: labelling per-route metrics by concrete path instead of pattern
  would create one Prometheus series per short code. At ten million codes that destroys the
  metrics backend, and it fails precisely when metrics are most needed.

### Decision: `knadh/koanf/v2` for configuration

- **Verified**: the required precedence — flags > env > file > default — by loading all
  four providers in order and confirming later loads win.
- **Alternatives considered**: `envconfig` and `caarlos0/env` (env-only; cannot layer file
  and flags); `viper` (heavier, afero and singleton baggage).

### Decision: a `Secret` type — with a documented hole

Implementing `String()`, `GoString()` and `MarshalJSON()` was verified to block leakage
through `%v`, `%+v`, `%#v`, `%s`, `%q`, `fmt.Println`, and `json.Marshal`.

- **The hole**: a plain `string(secret)` cast bypasses all of it. Type design alone cannot
  close this, so it needs a lint rule, not reviewer discipline. Recorded as an explicit
  task rather than left as an assumption that FR-049 holds.

### Decision: metrics buckets tuned to our SLOs

`{.001, .002, .005, .01, .02, .03, .05, .075, .1, .25, .5, 1}` seconds.

- **Rationale**: Prometheus's default buckets span 5ms–10s and would place our entire
  latency distribution in the first two buckets, making p99 unmeasurable at exactly the
  granularity SC-003 (50ms) and SC-006 (20ms) are specified in.

### Decision: `/metrics` binds to a separate, off-by-default internal address

Default `127.0.0.1:9090`, not the public listener.

- **Rationale**: a better fit for Principle VI than either exposing it publicly or bolting
  authentication onto a Prometheus scrape target, which scrapers handle poorly.

### Decision: shutdown sequence

`signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)` → bounded `srv.Shutdown(ctx)`
(~15s) → **then** a separately bounded analytics flush (~5s).

- **Rationale for the ordering**: running the flush after `Shutdown` returns means a stuck
  analytics sink cannot block request drain. Two independent budgets, not one shared one.
- **Documented gotcha**: `Shutdown` closes listeners first, then waits for in-flight
  requests to idle, but explicitly does **not** wait for hijacked connections. Relevant if
  SSE or WebSocket is ever added.

### Decision: multi-stage Dockerfile + buildx, base `gcr.io/distroless/static-debian13:nonroot`

- **Verified**: distroless `static` already bundles CA certificates and tzdata. Bare
  `scratch` would need both added manually — without CA certs **every S3 call over TLS
  would fail** — and has no nonroot mechanism for the non-root requirement.
- **Reproducibility**: `-trimpath`, empty `-buildid`, and `SOURCE_DATE_EPOCH`-driven
  `--rewrite-timestamp`.
- **Alternatives considered**: `ko` (a good technical fit, but a Dockerfile keeps ldflags
  and embed behaviour auditable in one visible place); `goreleaser` for images.

### Decision: CI shape

Parallel jobs — build; vet + gofmt; `golangci-lint-action` v9; `go test -race`;
integration tests against Postgres and MinIO as GitHub Actions `services:` containers; and
a `benchstat` gate comparing PR against base (`-count=10`), failing on any non-`~` positive
delta for the two constitutionally protected hot paths.

### Decision: JSONL export, one file per entity, optionally tarred with a manifest

- **Rationale**: streams without loading everything into memory. Rejected: a single JSON
  document (memory and streaming complexity); a SQL dump (backend-specific — it would fail
  the cross-backend requirement outright); CSV (lossy typing, no nesting).

### Decision: version info via `-ldflags -X`, with `ReadBuildInfo()` as secondary diagnostic

- **Verified**: shallow CI checkouts leave `ReadBuildInfo()`'s VCS fields empty and report
  `Main.Version` as devel-style for a `main` package build, so it cannot be the primary
  source.

---

## 7. Short codes and aliases (design, not library research)

Full derivation in the plan. Summary of decisions:

- **Generated codes**: 12 characters, lowercase Crockford base32 (excludes `i`, `l`, `o`,
  `u`), from `crypto/rand` = 60 bits.
- **Sizing rationale**: entropy is set by *enumeration resistance*, not collision. FR-013
  already mandates retry-on-collision, making collisions a cost question. The real threat
  is harvesting every destination on an instance. At 12 characters and the spec's 10M-code
  envelope, an attacker sustaining 1,000 guesses/sec expects ~3,600 years per hit; at 7
  characters it would be 1 hit per 3,436 guesses.
- **One namespace** shared by generated codes and aliases, with one case-insensitive
  unique index.
- **Aliases must not match the generated shape** (`^[0-9a-hjkmnp-tv-z]{12}$`), so the two
  kinds stay distinguishable and the generator's space cannot be poisoned.
- **Alias rules**: 3–64 chars, `[a-z0-9-]`, alphanumeric first and last, no consecutive
  hyphens (blocks the `xn--` punycode prefix), lowercased before comparison and storage.
- **Reserved-word list** covering instance routes, operational endpoints, auth paths,
  resource names, well-known files, and abuse-adjacent words (`verify`, `secure`,
  `billing` — a printed `/r/verify-account` is a credible phishing lure). A test asserts
  every route the router registers appears in the list, because that list rots silently.
- **Retired aliases are not reusable** without explicit admin release. A deleted campaign's
  alias, re-registered by someone else, would hand them every flyer already in the world.
- **Collision handling**: insert and let the unique index reject; retry up to 5 times.
  Never check-then-insert — that is a race two concurrent creates will lose, and the
  database constraint is the only authority that holds identically on both backends.

---

## Open items carried into Phase 1

1. **Constitution amendment to v1.0.1** — restate Principle II's migration clause as one
   ordered migration *sequence*. Required by the goose finding.
2. **In-house module shape renderer** — sized explicitly in the plan; it is the largest
   piece of work no dependency provides.
3. **`string(secret)` lint rule** — a task, since the type system cannot enforce FR-049.
