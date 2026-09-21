# Validation results

Validated 2026-09-06 on macOS arm64 with Go 1.26 and Node 24.19.

- `go test ./...` — PASS
- `go test -race ./...` — PASS
- `go vet ./...` — PASS
- `golangci-lint run ./...` — PASS (0 issues)
- `gofmt -l .` and `git diff --check` — PASS
- `CGO_ENABLED=0 go build -trimpath ./cmd/qurator` — PASS
- `QURATOR_TEST_BROWSER_CHANNEL=chrome npm test --prefix tests/browser` — PASS (4 tests)

The browser suite starts the real binary with isolated SQLite/filesystem storage and a
configured bootstrap account, blocks non-self requests, independently decodes the
downloaded PNG, exercises form and network errors, repeated optimistic edits, token
copy success/failure, revocation, shared API/console throttling, and destructive-action
confirmation. The revocation assertion waits within the existing 60-second token-cache
propagation budget. PostgreSQL/S3 contract tests were not configured in this run; the
repository's conditional suites and CI job remain responsible for that coverage.
