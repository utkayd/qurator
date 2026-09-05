# Plan: Direct storage URLs and batch creation
**Branch** `003-storage-urls-batch` · stacked on 002 · Constitution v1.0.1: Principle IV unaffected (image route stays public; disabling it is opt-in per VI); Principle II: URL capability is an optional interface on BlobStore, detected by type assertion, contract-tested where implemented.

## Tasks
- [x] T301 [P] Contract tests: storage URL fields per url_mode (public + presigned via memblob implementing URLer; fs → absent), `/i/` 404 when serving disabled, startup refusals
- [x] T302 [P] Contract tests: batch create — per-item results, `client_ref` idempotency (existing), conflict on changed destination, over-limit 413, empty 400, atomic on store failure
- [x] T303 Config: `images.*`, `codes.batch_max`, `codes.batch_workers`; validation rules
- [x] T304 `blob.URLer` interface; s3blob implements `PublicURL`/`PresignedURL`; memblob implements for tests; blobtest optional sub-suite runs when the driver implements it
- [x] T305 Migration 0004: `codes.client_ref TEXT NULL` + unique index `(user_id, client_ref)` where not null; store `CreateCodes(ctx, []*Code) error` atomic; `GetCodeByClientRef`; memstore/sqlite/postgres; storetest Req15
- [x] T306 `codes.Service`: `ImageURL`/`StorageURL` per mode; `CreateBatch` with bounded parallel render then one transaction
- [x] T307 v1 handlers: `image_url`/`storage_url` in every code JSON; `POST /v1/codes/batch`; `/i/` gated by config; OpenAPI + errors.md
- [x] T308 Console: storage URL with copy button on detail page
- [x] T309 Backend-parity steps; S3 contract run fetches the public URL; README section; full suite + lint
