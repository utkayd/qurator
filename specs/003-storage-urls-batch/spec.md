# Feature Specification: Direct storage URLs and batch creation

**Feature Branch**: `003-storage-urls-batch` (stacked on `002-direct-codes`)
**Created**: 2026-09-05 · **Status**: Draft

**Input**: "Say we want to get hundreds of QR codes via the API and get the S3 URLs of the codes as responses. Routing via us to the images should also be optional — what if we want the direct S3 URL?"

## Motivation
Today every image URL the API returns points at this instance's `/i/{id}.png`. Operators who
store images in S3 and serve them from the bucket or a CDN — or who simply do not want the
instance in the image path — have no way to get the object's own address. And creating
many codes means many round trips with no protection against duplicate creation on retry.

## User Scenarios & Testing

### US1 — Get the storage URL instead of the instance URL (P1)
An operator configures a public base URL for their bucket (or a presign TTL). Every code
response now carries the object's own address, and they can hand it out with no qurator
in the path.
**Independent Test**: with `images.url_mode=public` and a base URL, create a code and
assert `image_url` starts with the base URL and ends with the blob key; fetch the URL from
an S3-compatible server and assert the bytes equal the stored image.
1. **Given** url_mode `instance` (default), **When** a code is created, **Then** `image_url`
   is `/i/{id}.png` on this instance and `storage_url` is present only if derivable.
2. **Given** url_mode `public` with a base URL, **When** a code is created or read, **Then**
   `image_url` is `<base>/<blob key>` and `storage_url` equals it.
3. **Given** url_mode `presigned` with a TTL, **When** a code is read, **Then** `image_url` is
   a signed link that fetches the object from the S3 endpoint and expires after the TTL.
4. **Given** the filesystem blob driver, **When** url_mode is `public` or `presigned`,
   **Then** startup is refused with a message naming the driver mismatch.
5. **Given** the filesystem driver and url_mode `instance`, **Then** `storage_url` is absent.

### US2 — Take the instance out of the image path entirely (P2)
**Independent Test**: with `images.serve_via_instance=false`, `GET /i/{id}.png` returns 404
and code responses still carry a working `storage_url`.
1. **Given** serving disabled, **When** `/i/{id}.png` is requested, **Then** `404 not_found`.
2. **Given** serving disabled and url_mode `instance`, **Then** startup is refused: there
   would be no way to reach any image.

### US3 — Create hundreds of codes in one request, safely (P3)
**Independent Test**: post a batch of 300 items with unique `client_ref`s; assert 300
created; repost the same batch; assert 300 returned unchanged and none duplicated.
1. **Given** a batch of up to the configured maximum, **When** posted, **Then** each item
   gets its own result: the created code, or an error envelope for that item, and the
   request as a whole succeeds with `207`-style per-item status.
2. **Given** an item whose `client_ref` was already created by this user, **When** posted
   again, **Then** the existing code is returned for that item with `status: existing`.
3. **Given** a batch larger than the maximum, **When** posted, **Then** `413` with the limit.
4. **Given** a batch, **When** the store fails mid-way, **Then** no partial set is left:
   the batch's metadata is written in one transaction.
5. **Given** a batch, **When** items include both modes and styling, **Then** each item is
   rendered with its own options, in parallel, bounded by a worker count.

### Edge cases
- A `client_ref` reused with a *different* destination → `409 conflict` for that item,
  pointing at the existing code; never silently ignored.
- Presigned URLs are computed per response and never stored.
- Public base URL with a trailing slash or without — normalised.
- Batch items share the caller's identity; ownership is per item as for single create.

## Functional Requirements
- **FR-201** `images.url_mode` ∈ {`instance` (default), `public`, `presigned`};
  `images.public_base_url`; `images.presign_ttl` (default 1h); `images.serve_via_instance`
  (default true). All read from config; invalid combinations refuse startup.
- **FR-202** Every code representation carries `image_url` per url_mode and `storage_url`
  whenever the blob driver can derive one (S3: public or presigned per url_mode, else public
  if a base URL is set, else presigned).
- **FR-203** `BlobStore` gains an optional `URLer` capability: `PublicURL(key)` and
  `PresignedURL(ctx, key, ttl)`; the S3 driver implements it; the filesystem driver does not.
- **FR-204** `GET /i/{file}` returns `404` when serving is disabled; the route stays public
  and unauthenticated when enabled.
- **FR-205** `POST /v1/codes/batch` accepts `{items: [CreateCodeRequest + client_ref?]}` up
  to `codes.batch_max` (default 500), returns `{results: [{index, status: created|existing|
  error, code?, error?}]}` with HTTP 200; empty batch → 400; over limit → 413.
- **FR-206** `client_ref` is unique per user; stored on the code; a repeat with the same
  destination+mode returns the existing code; a repeat with different destination → per-item
  `409 conflict` with `details.existing_id`.
- **FR-207** Batch metadata is persisted atomically; images are rendered in parallel with
  `codes.batch_workers` (default 4) bounded concurrency; rendering failures surface per item
  and remove that item from the transaction.
- **FR-208** Console detail page shows `storage_url` with a copy button when present.

## Success criteria
- **SC-201** With url_mode `public`, 100% of responses carry a URL that fetches the image
  from the S3 endpoint with no request to the instance (asserted in the S3 contract run).
- **SC-202** A 300-item batch completes in under 5 s on the default stack and a repeat of
  the same batch creates zero new rows.
- **SC-203** Backend-parity matrix gains batch and storage-URL steps and stays identical.

## Assumptions
- Batches are synchronous with a cap; an async job model is deferred until a real need
  exceeds the cap.
- `client_ref` is opaque, ≤ 128 chars; uniqueness is per user, like everything else.
- Presigned URLs use the configured S3 endpoint; a CDN in front of a private bucket is
  the `public` mode's job.
