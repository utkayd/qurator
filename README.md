# qurator

A self-hosted, single-binary dynamic QR code service. Generate QR codes whose
destination can change after they're printed, track scans without ever storing a
scanner's IP address, and walk away with your data in one command whenever you want.

## Start here: one command, zero configuration

```bash
./bin/qurator
```

That's it. No database to provision, no object store to wire up, no identity provider
to register with. qurator starts with SQLite as its metadata store and the local
filesystem as its blob store, both created on first run, and serves on `:8080`.

```bash
curl -fsS localhost:8080/healthz   # 200 once the process is up
curl -fsS localhost:8080/readyz    # 200 once its own storage is reachable
```

This isn't a "quick start that becomes a liability later" — it's Principle I of the
project's constitution: **a person MUST be able to run a useful qurator instance with
one command, one binary, and zero external services.** PostgreSQL and S3-compatible
storage exist as an *upgrade* (below), never as a prerequisite. If a feature can't work
on the default stack, that's a bug, not an acceptable tradeoff.

Prebuilt binaries: see [Releases](https://github.com/utkayd/qurator/releases), or run
the container:

```bash
docker run -p 8080:8080 -v qurator-data:/data ghcr.io/utkayd/qurator:latest
```

## Two modes

qurator does two structurally separate things, and it's deliberate that they don't
share a code path:

- **Ephemeral generation** (`GET /v1/qr`) renders a QR code from whatever content you
  send and returns the image. Nothing is written to storage — no database row, no blob.
  Same input always produces the same bytes, so it's safe to cache. Requires a
  credential by default; an operator can opt into unauthenticated access for this
  endpoint specifically, subject to a rate limit.
- **Dynamic codes** (`POST /v1/codes`) persist a short code whose destination you can
  change after the code is printed. Scanning `/r/{code}` issues a `302` (never a
  `301`/`308` — see *Privacy and correctness* below) to the current destination and
  records an anonymous, aggregate-only scan event.

`internal/qr`, the ephemeral rendering package, is structurally forbidden from
importing any storage package at all — it's enforced by an architecture test, not just
a convention — so "ephemeral" actually means ephemeral.

## Upgrading to PostgreSQL and S3-compatible storage

Nothing about the API or behavior changes; only where the bytes live does.

```bash
docker compose -f deploy/compose.yaml up -d     # PostgreSQL 16 + MinIO, for local use

QURATOR_DB_DRIVER=postgres \
QURATOR_DB_DSN='postgres://qurator:qurator@localhost:5432/qurator?sslmode=disable' \
QURATOR_BLOB_DRIVER=s3 \
QURATOR_BLOB_S3_ENDPOINT=localhost:9000 \
QURATOR_BLOB_S3_BUCKET=qurator \
QURATOR_BLOB_S3_ACCESS_KEY=qurator \
QURATOR_BLOB_S3_SECRET_KEY=qurator-dev-secret \
QURATOR_BLOB_S3_USE_SSL=false \
QURATOR_BLOB_S3_PATH_STYLE=true \
  ./bin/qurator
```

Every config value above is also settable via a YAML file (`--config` or
`QURATOR_CONFIG`) or a matching `--flag`; precedence is flags > env > file > defaults.

## Sitting behind a reverse proxy (forward-auth)

qurator accepts an upstream-asserted identity header instead of running its own
sign-in, for use behind `oauth2-proxy`, Authelia, or similar. Trust is anchored to the
**TCP peer address** that made the request, never to the header's own content or to
`X-Forwarded-For` — a request claiming to be `admin@example.com` from an untrusted peer
is rejected outright, and a request carrying the header twice is rejected too (an
attempted smuggle, not a value to pick between).

```bash
QURATOR_FORWARD_AUTH_ENABLED=true \
QURATOR_FORWARD_AUTH_HEADER=X-Forwarded-Email \
QURATOR_FORWARD_AUTH_TRUSTED_CIDRS=172.20.0.0/16 \
  ./bin/qurator
```

`QURATOR_FORWARD_AUTH_TRUSTED_CIDRS` is not optional in practice: forward-auth with an
empty trust list fails closed at startup rather than silently trusting everyone.

### oauth2-proxy

```yaml
# oauth2-proxy config, upstream pointed at qurator
upstreams:
  - id: qurator
    path: /
    uri: http://qurator:8080
pass_authorization_header: false
set_xauthrequest: true
# oauth2-proxy sets X-Forwarded-Email by default when set_xauthrequest is true.
```

Run qurator only reachable from oauth2-proxy's container/network, and set
`QURATOR_FORWARD_AUTH_TRUSTED_CIDRS` to that network's CIDR — not `0.0.0.0/0`.

### Authelia

```yaml
# Authelia access control, in front of qurator
access_control:
  default_policy: deny
  rules:
    - domain: qurator.example.com
      policy: one_factor
```

Authelia's forward-auth response sets `Remote-Email`; point
`QURATOR_FORWARD_AUTH_HEADER` at whatever header your proxy tier actually forwards that
value as (e.g. `X-Forwarded-Email` after an nginx `auth_request` rewrite), and again
scope `QURATOR_FORWARD_AUTH_TRUSTED_CIDRS` to the proxy's own address, not the internet.

## Privacy, by construction

- Redirects are `302 Found` with `Cache-Control: no-store, no-cache, must-revalidate`.
  Never `301`/`308`: those are heuristically cached forever by browsers and
  intermediaries, which would silently keep sending scanners to a stale destination
  after you change it and would stop most repeat scans from ever being counted.
- No scanner IP address is ever recorded — there is no column for one, in any
  supported backend. Referrers are stored as host only (`example.com`, not the full
  URL with its query string).
- No geographic analytics in v1. It was deliberately cut: every plausible source of
  geo data (IP geolocation databases, third-party APIs) would have made an external
  dependency compulsory, which Principle I forbids.
- No telemetry leaves your instance. `/metrics` is Prometheus-format and, by default,
  bound only to `127.0.0.1` on a separate internal port — not exposed on the public
  listener at all unless you explicitly configure `QURATOR_SERVER_METRICS_LISTEN`.

## Export and import

Walk away with everything, in one file, at any time:

```bash
./bin/qurator export --out ./dump/          # writes ./dump/export.tar
./bin/qurator import --in ./dump/           # into a fresh instance
```

The archive is a tar file containing `manifest.json` (format version, row counts per
entity, and anything that couldn't be exported) plus one `<entity>.jsonl` file per
entity — never a backend-specific SQL dump, so it round-trips across a SQLite-to-Postgres
upgrade unchanged. It streams in both directions: exporting a store with a million rows
does not hold a million rows in memory at once.

**What it deliberately leaves out, and why:**

- User passwords and API token secrets are never written to an export. A user restored
  from an export has no usable local password until one is reset (or simply use
  forward-auth for it); a restored token record is informational only — it cannot
  authenticate, and re-importing one does not attempt to.
- `import` refuses to run against a store that already has users, unless you pass
  `--force` — it's an "into a fresh instance" tool, not a merge tool.

The same dump is also available live over HTTP, admin-only:

```bash
curl -fsS -H "Authorization: Bearer $ADMIN_TOKEN" localhost:8080/v1/export -o export.tar
```

## Build from source

```bash
CGO_ENABLED=0 go build -trimpath -o bin/qurator ./cmd/qurator
file bin/qurator      # must report a statically linked binary
```

`CGO_ENABLED=0` isn't an optimization flag here — it's the reason the SQLite driver is
`modernc.org/sqlite` (pure Go) rather than `mattn/go-sqlite3`, and it's what makes the
single-binary promise literally true rather than true-until-you-cross-compile.

Or build the container image directly:

```bash
docker build -f deploy/Dockerfile -t qurator:local .
```

## Contributing

```bash
go test -race ./...          # green with no Docker running; Postgres/S3 suites skip
go vet ./...
gofmt -l .                   # must print nothing
```

The default test run is Docker-free by design (skipped suites report themselves as
skips, never as silent passes). The Postgres/S3 contract suites need real backends —
bring them up locally and export the DSNs they're looking for:

```bash
docker compose -f deploy/compose.yaml up -d postgres minio
QURATOR_TEST_PG_DSN='postgres://qurator:qurator@localhost:5432/qurator?sslmode=disable' \
QURATOR_TEST_S3_ENDPOINT=localhost:9000 \
  go test -race ./...
```

CI runs both: the Docker-free suite on every push and PR
(`.github/workflows/ci.yml`), and the same Postgres/S3 suites against real service
containers (`.github/workflows/contract-tests.yml`) so nothing that's skipped locally
goes unexercised entirely.
