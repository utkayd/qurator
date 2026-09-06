# Browser regression tests

These tests compile and start the real qurator binary with a temporary SQLite database,
filesystem blobs, configured bootstrap account, generated signing key, and loopback origin.
No existing instance or data directory is used. A separate compiled gozxing decoder
verifies the downloaded image. Browser fixtures exercise the actual embedded HTMX and CSP;
only clipboard outcomes and deliberate network/server failures are simulated.

Requires Go from `go.mod` and Node 20+ (Node 24 in CI). From the repository root:

```sh
npm ci --prefix tests/browser
npm --prefix tests/browser exec -- playwright install chromium
npm test --prefix tests/browser
```

With an installed Google Chrome, skip the browser download and run:

```sh
QURATOR_TEST_BROWSER_CHANNEL=chrome npm test --prefix tests/browser
```

Playwright and its browser are test-only dependencies; `go test ./...`, the static build,
and deployed qurator need neither. CI runs the browser suite in a separate job.
Failures retain traces in `tests/browser/test-results/`; these may contain temporary
test credentials, so keep them local or in private CI artifacts. The revocation check
allows the existing 60-second credential-cache propagation bound (001 SC-011).
