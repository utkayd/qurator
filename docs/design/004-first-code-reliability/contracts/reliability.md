# Reliability contracts

- `POST /v1/codes`: missing scan origin on new dynamic creation returns 503 `scan_url_not_configured`, safe guidance, no rows/blobs. `mode: direct` works with empty origin. Existing `client_ref` replay stays idempotent.
- `POST /v1/codes/batch`: still HTTP 200; affected dynamic items return that error, direct items can succeed, existing items return existing.
- Config: nonempty `server.base_url` must be an absolute HTTP(S) origin with a host and optional valid port/root slash; no userinfo/path/query/fragment.
- Both sign-in POST routes share peer quota and emit 429/Retry-After; console rejection is safe HTML.
- Expected console HTML errors carry `X-Qurator-Form-Error: true` and are inserted into a local `[data-form-error]` region. Unexpected responses are never inserted as HTML.
- Successful destination PATCH returns `ETag: "<version>"`; browser updates only the submitting form's optimistic version.
- No new public scan routes or auth middleware; no changed metadata schemas.
