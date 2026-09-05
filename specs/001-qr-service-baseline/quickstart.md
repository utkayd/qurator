# Quickstart & Validation Guide: qurator v1

**Feature**: [spec.md](./spec.md) | **Plan**: [plan.md](./plan.md)

Runnable scenarios that prove the feature works end to end. Each maps to a user story and
its success criteria. This is a validation guide — implementation lives in `tasks.md`.

## Prerequisites

- Go 1.26+
- **Nothing else.** No database, no object store, no Docker. That is the point of
  Scenario 1 and it is Constitution Principle I made checkable.

Optional, only for the upgrade path in Scenario 7: Docker for PostgreSQL and MinIO.

## Build

```bash
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/qurator ./cmd/qurator
file bin/qurator      # must report a statically linked binary
```

`CGO_ENABLED=0` is not an optimisation here. It is what makes the single-binary promise
true, and it is why the SQLite driver had to be pure Go.

---

## Scenario 1 — Zero-configuration start (US1, US7 · SC-001, SC-002)

```bash
env -i PATH=/usr/bin:/bin ./bin/qurator
```

`env -i` clears the entire environment deliberately: it proves no configuration is
required, rather than proving your shell happens to have the right variables set.

**Expected**: starts in under a second; logs the SQLite path and the local blob directory
it created; logs `generated signing secret` with the path `data/signing.key`; serves on
the default port. No error, no missing-dependency warning.

```bash
curl -fsS localhost:8080/healthz    # 200
curl -fsS localhost:8080/readyz     # 200
stat -f '%Lp' data/signing.key      # 600 (Linux: stat -c '%a')
```

**Then prove the secret persists** (FR-040): stop the process, start it again in the
same directory, and confirm the log line `generated signing secret` does NOT appear a
second time and `data/signing.key` is byte-identical. Sessions issued before the restart
still verify after it. `data/signing.key` is part of the instance's state: back it up
with the database, or set `QURATOR_AUTH_SIGNING_SECRET` to manage the secret yourself.
Pointing `QURATOR_SERVER_DATA_DIR` at an unwritable location with no secret configured
must refuse to start with a message naming `QURATOR_AUTH_SIGNING_SECRET`.

---

## Scenario 2 — Ephemeral generation, storing nothing (US1 · SC-002, SC-006, SC-007)

```bash
curl -fsS -H "Authorization: Bearer $QURATOR_TOKEN" \
  "localhost:8080/v1/qr?content=hello&format=png" -o /tmp/hello.png

go run ./tools/qrdecode /tmp/hello.png     # must print exactly: hello
```

**Then prove nothing was stored** — this is the actual assertion, not the image:

```bash
sqlite3 "$QURATOR_DB" "select count(*) from codes;"        # 0
find "$QURATOR_BLOB_DIR" -type f | wc -l                   # 0
```

**Determinism** (FR-004, required for cacheability):

```bash
curl -fsS ... -o /tmp/a.png && curl -fsS ... -o /tmp/b.png
cmp /tmp/a.png /tmp/b.png     # identical
```

---

## Scenario 3 — Change a printed code's destination (US2 · SC-008)

```bash
CODE=$(curl -fsS -X POST localhost:8080/v1/codes \
  -H "Authorization: Bearer $QURATOR_TOKEN" -H 'Content-Type: application/json' \
  -d '{"destination":"https://example.com/a"}' | jq -r .short_code)

curl -sI "localhost:8080/r/$CODE" | grep -i '^location'   # → https://example.com/a
```

Change it, and re-scan **the same code**:

```bash
curl -fsS -X PATCH "localhost:8080/v1/codes/$ID" ... -d '{"destination":"https://example.com/b"}'
curl -sI "localhost:8080/r/$CODE" | grep -i '^location'   # → https://example.com/b
```

**Also assert the caching headers**, because this is where a wrong status silently breaks
the feature:

```bash
curl -sI "localhost:8080/r/$CODE" | grep -iE '^(HTTP|cache-control)'
# HTTP/1.1 302 Found
# Cache-Control: no-store, no-cache, must-revalidate
```

A `301` here would pass the redirect test and still break the product: browsers cache it
indefinitely, so the destination change would never reach anyone who had already scanned,
and repeat scans would stop being recorded.

---

## Scenario 4 — Security properties (US2, US3 · FR-011, FR-012, FR-018, FR-037)

Each of these must be **rejected**:

```bash
# Hostile scheme
-d '{"destination":"javascript:alert(1)"}'            # 400 unsupported_scheme
# Redirect loop back into ourselves
-d '{"destination":"http://localhost:8080/r/abc"}'    # 400 self_referential_destination
# Reserved alias shadowing a real route
-d '{"alias":"healthz"}'                              # 409 alias_reserved
# Alias of a DELETED code — the printed-flyer hijack
-d '{"alias":"<alias of a deleted code>"}'            # 409 alias_taken
```

**Forward-auth must ignore an untrusted assertion:**

```bash
# forward-auth ENABLED, trusted CIDR set to something that excludes us
curl -sS -H 'X-Forwarded-Email: admin@example.com' localhost:8080/v1/codes   # 401
```

If this returns `200`, the instance is an open login for anyone who can reach the port.
This assertion matters more than any other in this file.

**Duplicate assertions must be refused, not resolved:**

```bash
curl -sS -H 'X-Forwarded-Email: a@x.com' -H 'X-Forwarded-Email: b@x.com' ...   # 401
```

---

## Scenario 5 — Analytics never block a redirect (US4 · SC-003, SC-005, SC-012)

With the analytics writer deliberately stalled (test build flag or a paused database):

```bash
hey -n 10000 -c 50 "localhost:8080/r/$CODE"
```

**Expected**: p99 stays under 50ms; the redirect keeps working; dropped events appear in
`qurator_scan_events_dropped_total`. A latency rise here is a Principle IV violation, not a
performance nit.

**Privacy assertion** — no IP anywhere:

```bash
sqlite3 "$QURATOR_DB" "select * from scan_events limit 5;"   # no address column, no address value
```

**Sum invariant** (FR-023):

```bash
# every dimension breakdown must total the overall count for the same range
curl -fsS ".../analytics?from=...&to=..." | jq '[.breakdowns[].values[].count] | add'
```

---

## Scenario 6 — Styling stays scannable (US5 · SC-007)

Render across every combination of shape × EC level × margin × size, decode each with the
independent decoder, and assert equality. Then confirm these are **rejected**:

```bash
"...&fg=%23fefefe&bg=%23ffffff"     # 400 contrast_too_low
"...&logo_scale=0.5&ec=L"           # 400 logo_too_large  (L allows 5%)
"...&size=100000"                   # 400 dimensions_exceeded
```

Verify `ec=L` is honoured exactly and not silently promoted — the underlying library
defaults to boosting it, which we disable.

---

## Scenario 7 — Backend swap changes nothing (US7 · SC-010, SC-013)

```bash
docker compose -f deploy/compose.yaml up -d      # PostgreSQL + MinIO

QURATOR_DB_DRIVER=postgres QURATOR_DB_DSN=... \
QURATOR_BLOB_DRIVER=s3 QURATOR_S3_ENDPOINT=... ./bin/qurator
```

Re-run Scenarios 2–6 unchanged. **Every result must be identical.** Any behavioural
difference is a Principle II violation.

**Export and re-import:**

```bash
./bin/qurator export --out /tmp/dump/
./bin/qurator import --in /tmp/dump/     # into a fresh instance
```

Every code, destination, styling profile, and scan aggregate must be present and identical.

---

## Scenario 8 — Shutdown loses nothing (US7 · SC-014)

Under sustained load, send `SIGTERM`. In-flight requests must complete, buffered scan
events must flush, and the process must exit cleanly. Assert zero non-2xx responses in the
load tool's output and that the post-restart scan count matches what was sent.

---

## Running the test suite

```bash
go test -race ./...                                   # green with no Docker; PG/S3 skip
QURATOR_TEST_PG_DSN=... QURATOR_TEST_S3_ENDPOINT=... go test -race ./...   # full coverage
go test -run XXX -bench . -benchmem ./internal/qr ./internal/httpapi/public
```

Skipped storage tests are reported as skips, not silently passed — a skip you cannot see is
worse than a failure.
