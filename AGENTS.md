# Contributor guide

qurator is a self-hosted, single-binary dynamic QR code service written in Go. This file
is the short guide for humans and coding agents working in the repository. The design
documents each feature was built from live in [`docs/design/`](docs/design/README.md);
they are history, not a to-do list.

## Standing rules for this codebase

These are settled decisions with evidence behind them. Re-litigating one wastes effort;
the reasoning is in `docs/design/001-qr-service-baseline/research.md` if you need it.

1. **`CGO_ENABLED=0` always.** The single static binary is Principle I. This is why the
   SQLite driver is `modernc.org/sqlite` and not `mattn/go-sqlite3`.
2. **`internal/qr` must never import a store package.** The ephemeral path is
   structurally forbidden from touching storage (Principle III). `tests/arch` enforces it.
3. **Public scan routes carry no auth middleware.** Not "auth that allows anonymous" —
   no auth middleware mounted at all (Principle IV).
4. **Redirects are `302` with `no-store`.** Never `301`/`308`: they are heuristically
   cached forever, which breaks destination changes and hides scans.
5. **API tokens hash with SHA-256; the admin password uses Argon2id.** This asymmetry is
   deliberate — 256-bit random tokens gain nothing from a slow KDF and it would add a
   CPU-exhaustion vector on the hot path.
6. **Forward-auth trust comes from the TCP peer, never from `X-Forwarded-For`.**
7. **Per-route metrics label by route *pattern*** (`r.Pattern`), never the concrete path.
   `/r/{code}` by path would create one Prometheus series per short code.
8. **No scanner IP is ever persisted.** There is no column for one. Referrers are stored
   as host only.
9. **No geographic analytics in v1** — deliberately cut; every source would have made an
   external dependency compulsory.
10. **Contract tests skip when `QURATOR_TEST_PG_DSN` / `QURATOR_TEST_S3_ENDPOINT` are
    unset.** `go test ./...` must stay green and Docker-free for contributors.
11. **Rejected libraries — do not reintroduce**: `go-pkgz/auth` (no Bearer, no
    revocation), `chi` (stdlib `ServeMux` exposes `r.Pattern`), `yeqown/go-qrcode` (its
    SVG writer does not exist).
12. **Test QR output by decoding it** with the independent decoder, never by byte
    snapshots. Binary payloads use `ResultMetadataType_BYTE_SEGMENTS`, not `GetText()`.

## Build and test

```bash
CGO_ENABLED=0 go build -trimpath -o bin/qurator ./cmd/qurator
go test -race ./...
go vet ./... && gofmt -l .
```

The browser regression suite in `tests/browser/` runs the real binary in Chromium; see
its README. `tests/e2e/` is the Go HTTP-with-fakes console suite and needs no browser.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
