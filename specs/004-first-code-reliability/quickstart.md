# Validation guide

Run `go test -race ./...` and `make build`; no browser is needed for Go tests. Browser setup and execution are documented in `tests/browser/README.md`. Run the browser suite against its isolated compiled-server fixtures, not an existing user instance. Verify invalid input and stale edits visibly fail, two successive valid edits work, token copy failure retains text, and confirmation cancel does not delete. Decode the downloaded PNG and follow its scan address after updates.

For operator setup, use the configured bootstrap email/password and `QURATOR_SERVER_BASE_URL` documented in README. Empty origin must still start but dynamic creation must fail clearly; direct and ephemeral generation remain available with configured authentication. Never print a localhost-origin dynamic code for use on another device.
