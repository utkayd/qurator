# Contract: Error Shape and Stable Code Catalogue

**Feature**: [../spec.md](../spec.md) — satisfies **FR-044**

Every error from every endpoint uses one shape. There is no second form, no bare string
body, and no HTML error page on an API route.

```json
{
  "error": {
    "code": "alias_taken",
    "message": "The alias 'spring-sale' is already in use.",
    "details": { "alias": "spring-sale" }
  }
}
```

- **`code`** is a stable, snake_case machine identifier. It is part of the API contract:
  once shipped it may not be renamed or repurposed within a major version. Clients branch
  on this.
- **`message`** is human-readable and may be reworded freely between releases. Clients must
  never parse it.
- **`details`** is optional and structured. It carries the offending value or the limit that
  was exceeded, so a caller can build a useful message without regex-ing `message`.

**`message` must never leak internals** (FR-044). No SQL text, no driver error strings, no
file paths, no stack traces, no upstream hostnames. The internal cause is logged with the
request ID; the client receives the code and a safe sentence. Logging the detail and
returning it are different acts, and only one of them is safe.

## Catalogue

| Code | HTTP | Meaning | `details` |
|------|------|---------|-----------|
| `invalid_request` | 400 | Malformed body, bad parameter, failed validation | `field` |
| `content_too_large` | 413 | Payload exceeds the encodable maximum | `limit_bytes`, `actual_bytes` |
| `unsupported_scheme` | 400 | Destination scheme not on the allow-list (FR-011) | `scheme`, `allowed` |
| `self_referential_destination` | 400 | Destination resolves to this instance's scan path (FR-012) | — |
| `alias_invalid` | 400 | Alias fails charset, length, or shape rules | `reason` |
| `alias_reserved` | 409 | Alias is on the reserved-word list | `alias` |
| `alias_taken` | 409 | Alias in use, or reserved by a deleted code (FR-018) | `alias` |
| `contrast_too_low` | 400 | Foreground/background contrast below threshold (FR-028) | `ratio`, `minimum` |
| `logo_too_large` | 400 | Logo exceeds the recoverable area for the EC level (FR-028) | `scale`, `max_scale`, `ec_level` |
| `dimensions_exceeded` | 400 | Requested output size above the configured bound (FR-029) | `requested`, `maximum` |
| `render_timeout` | 400 | Rendering exceeded its time budget (FR-029) | `timeout_ms` |
| `not_found` | 404 | No such resource, **or** not visible to this caller | — |
| `code_disabled` | 410 | The code exists but is disabled or deleted | — |
| `unauthorized` | 401 | No credential, or an invalid one | — |
| `forbidden` | 403 | Authenticated but not permitted | — |
| `token_revoked` | 401 | The presented token was revoked (FR-035) | — |
| `conflict` | 409 | Concurrent modification lost the race | `expected`, `actual` |
| `direct_code_immutable` | 409 | Destination/state change on a direct code, whose destination is encoded in the printed image (spec 002) | `mode` |
| `not_tracked` | 400 | Analytics requested for a direct code; scans never pass through the instance (spec 002) | `mode` |
| `batch_too_large` | 413 | Batch exceeds `codes.batch_max` (spec 003) | `limit`, `actual` |
| `client_ref_conflict` | 409 | `client_ref` already used by this user for a different destination or mode (spec 003) | `client_ref`, `existing_id` |
| `rate_limited` | 429 | Rate limit exceeded | `retry_after_s` |
| `internal` | 500 | Unexpected failure; details are logged, not returned | — |

## Deliberate decisions

**`not_found` covers "exists but is not yours."** Returning `403` for another user's code
would confirm that the ID exists, turning the endpoint into an existence oracle that
enumerates other users' codes. The contract test in
[store.md](./store.md) pins this at the storage layer so a handler cannot reintroduce the
leak by accident.

**`token_revoked` is distinct from `unauthorized`.** Both are `401`, but a CI pipeline that
suddenly fails deserves to learn its token was revoked rather than guess at a
misconfiguration. The distinction costs nothing and reveals nothing an attacker could not
already determine by presenting the token.

**`code_disabled` is `410 Gone`, not `404`.** A disabled code is a different situation from
one that never existed, and `410` is the honest status. This is the API surface — the
*public scan path* deliberately does not do this: `GET /r/{code}` returns a human landing
page for unknown, disabled, and deleted codes alike (FR-014), because the audience there is
a person holding a phone, not a program, and distinguishing the cases for them would leak
which codes have existed.

**Rate limiting returns `retry_after_s` in `details` as well as the `Retry-After`
header.** The header is correct HTTP; the field means a client that already parses this
error shape does not need special-case header handling to behave well.
