# Feature Specification: First-code reliability

**Created**: 2026-09-06 · **Status**: Implemented and locally validated
**Input**: Implement the reliability milestone from the spec-aligned codebase review: console sign-in throttling, visible validation errors, HTMX initialization, safe clipboard behavior, and valid dynamic scan URLs.

## User Scenarios & Testing

### US1 — Create a code that works when printed (P1)
An operator starts the instance with configured bootstrap credentials and a public origin, creates a dynamic code, downloads it, and changes its destination without reprinting.

**Independent test**: create with an absolute origin, independently decode the downloaded image, follow its scan URL, edit twice, and verify the same image leads to the latest destination.

1. Empty origin permits startup and direct/ephemeral generation but refuses new dynamic creation before rendering or storage, with an actionable error.
2. A configured origin accepts HTTP(S), hostname or IP, optional valid port, and an optional trailing slash. Userinfo, path prefixes, query strings, fragments, missing hosts and invalid ports are rejected at startup.
3. Dynamic creation through single, batch, and console paths uses the same rule. Request Host/forwarded headers cannot supply the printed origin.
4. Existing codes and idempotent retries remain readable; this change does not rewrite previously printed artifacts.

### US2 — Sign in with consistent protection (P1)
An operator uses either sign-in entry point; attackers cannot bypass throttling by alternating them.

**Independent test**: exhaust the shared per-peer allowance across API and console; both reject the next attempt before credential verification, while unrelated routes remain available.

1. Both password sign-in routes share the existing ten-attempts-per-minute peer bucket.
2. Rejection returns 429 and Retry-After. Console users receive a visible message.
3. Rate-limit identity remains the TCP peer; forwarded client headers are ignored.

### US3 — Complete the console lifecycle and recover from mistakes (P1)
A user sees errors, corrects input, copies a token, and confirms destructive actions without reloading to repair controls.

**Independent test**: real-browser flow against the compiled app and default storage: invalid create, correction, download, repeat edits, stale conflict, token creation/copy/revoke, and confirmed deletion.

1. Expected validation and conflict responses are displayed accessibly and preserve entered values. Unexpected/server/network failures show a safe generic message without replacing the form.
2. Newly inserted controls work after partial/full-content swaps, without duplicate handlers. A successful edit advances the form's version; a genuine stale edit remains rejected.
3. Token text is hidden only after clipboard success. Denial or unavailable clipboard leaves selectable text with manual-copy guidance.
4. Destructive form submissions use the existing confirmation dialog; cancellation performs no mutation.

## Functional Requirements

- **FR-301**: Validate configured scan origin and prevent newly created dynamic images with a relative or invalid address (001 FR-007/SC-008; 002 FR-102). Empty origin produces `503 scan_url_not_configured` for single creation, and the same per-item error in HTTP-200 batches. Direct mode does not need an origin.
- **FR-302**: Share sign-in throttling across console/API, before password verification (001 T113 and Principle VI).
- **FR-303**: Display console validation, conflict, rate-limit and unexpected failures; preserve recoverable input and update optimistic version only after success (001 FR-042/SC-009).
- **FR-304**: Initialize inserted controls safely; honor confirmation and clipboard failure without false success (001 FR-033/US6).
- **FR-305**: Add real-browser regression coverage against the actual application while retaining a browser-free `go test ./...`. CI must execute the browser suite independently.
- **FR-306**: Document configured bootstrap and public-origin setup; preserve secure defaults, offline assets, CSP and direct-code immutability. No account reset, image restyling, asynchronous batch jobs, or external services are introduced.

## Key Entities

No schema changes. Existing instance origin is validated; existing code version remains the concurrency token. Browser UI state and transient peer buckets are not persisted.

## Success Criteria

- **SC-301**: Every successful new dynamic artifact decodes to the configured absolute scan address; rejected creations leave no rows or blobs.
- **SC-302**: Alternating sign-in paths cannot exceed their shared allowance; scan/image requests remain ungated.
- **SC-303**: All listed browser recovery and lifecycle scenarios pass with real storage; copy failure never removes the secret.
- **SC-304**: Default startup, existing Go tests, race tests, static build, formatting and lint remain valid; no external asset request is needed by the console.

## Clarifications and Assumptions

The user's approval authorizes the scoped fixes and tests. Origin validation is a correctness fix for previously unusable printed addresses; it intentionally rejects malformed nonempty configuration. Root-path hosting is the current route contract; subpath hosting needs a separate feature. Empty origin is not a startup error, preserving zero-config startup and useful direct/ephemeral capability after configured authentication. Bootstrap remains configuration-seeded (001 FR-032); no registration or password recovery flow. SVG downloads and the other review findings remain outside this milestone.
