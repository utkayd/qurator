<!-- SPECKIT START -->
For additional context about technologies to be used, project structure,
shell commands, and other important information, read the current plan:
`specs/005-docker-packaging/plan.md`

Supporting artifacts:
- `.specify/memory/constitution.md` — project principles (v1.0.1). Non-negotiable.
- `specs/001-qr-service-baseline/spec.md` — 56 functional requirements
- `specs/001-qr-service-baseline/research.md` — Phase 0 findings and stack reversals
- `specs/001-qr-service-baseline/data-model.md` — entities, indexes, dialect differences
- `specs/001-qr-service-baseline/contracts/` — OpenAPI, Store/BlobStore, error catalogue
- `specs/001-qr-service-baseline/quickstart.md` — end-to-end validation scenarios

## Standing rules for this codebase

These are settled decisions with evidence behind them. Re-litigating one wastes effort;
the reasoning is in `research.md` if you need it.

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
<!-- SPECKIT END -->
