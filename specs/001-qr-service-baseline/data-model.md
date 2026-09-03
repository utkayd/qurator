# Data Model: qurator v1

**Feature**: [spec.md](./spec.md) | **Plan**: [plan.md](./plan.md) | **Date**: 2026-09-04

Derived from the spec's Key Entities and the Phase 0 findings. Every table below is
expressed once as a logical model, with a **Dialect** note wherever SQLite and PostgreSQL
must genuinely differ. Per Constitution v1.0.1, those differences live inside a single
numbered migration that branches its DDL — not in two migration sets.

## Conventions

- **Identifiers**: every externally visible entity uses an opaque, unguessable string ID,
  not a sequential integer. Sequential IDs in URLs leak volume and permit enumeration.
- **Timestamps**: stored UTC. Dialect: `TEXT` in RFC3339 for SQLite, `TIMESTAMPTZ` for
  PostgreSQL, normalised by the driver so callers above `store` see `time.Time` only.
- **Booleans**: `INTEGER` 0/1 in SQLite, `BOOLEAN` in PostgreSQL; driver-normalised.
- **Deletes**: soft where a printed artefact depends on the row (codes, aliases); hard
  where it does not (sessions, expired scan events).
- **No scanner IP is stored anywhere in this model.** There is deliberately no column for
  it, so FR-022 cannot be violated by a later careless insert.

---

## Entity: `users`

One row per identity. v1 is single-tenant; the table exists so multi-tenancy is an added
column rather than a redesign.

| Column | Type | Constraints | Notes |
|--------|------|-------------|-------|
| `id` | TEXT | PK | `usr_` + 16 random base32 chars |
| `email` | TEXT | NOT NULL, UNIQUE (case-insensitive) | Also the forward-auth join key |
| `password_hash` | TEXT | NULL | PHC-encoded Argon2id. NULL for forward-auth-only users |
| `is_admin` | BOOL | NOT NULL, default false | Replaces the rejected library's RBAC |
| `token_version` | INTEGER | NOT NULL, default 0 | Bump invalidates every session at once |
| `source` | TEXT | NOT NULL | `local` \| `forward_auth` |
| `created_at` | TIMESTAMP | NOT NULL | |
| `last_login_at` | TIMESTAMP | NULL | |

- **Dialect**: case-insensitive `email` uniqueness — SQLite `COLLATE NOCASE`; PostgreSQL
  `UNIQUE INDEX ON (lower(email))` with queries via `lower()`. `CITEXT` rejected: it needs
  `CREATE EXTENSION`, a privilege managed-database users may lack.
- **Validation**: `password_hash` NULL is only legal when `source = 'forward_auth'`. A
  local user with no password could otherwise be created and then never authenticate,
  or worse, match an empty-password code path.
- **Bootstrap**: exactly one `is_admin` local user is seeded on first start from config
  (FR-032). Seeding is conditional on the table being empty — never on a marker file,
  which a volume reset would clear, silently re-seeding a fresh admin password.

## Entity: `api_tokens`

| Column | Type | Constraints | Notes |
|--------|------|-------------|-------|
| `id` | TEXT | PK | `tok_` + 16 random base32 chars; safe to log |
| `user_id` | TEXT | NOT NULL, FK → `users.id` | |
| `name` | TEXT | NOT NULL | Human label |
| `secret_hash` | BLOB | NOT NULL | **SHA-256** of the token, not Argon2id |
| `created_at` | TIMESTAMP | NOT NULL | |
| `last_used_at` | TIMESTAMP | NULL | Written lazily, not on every request |
| `revoked_at` | TIMESTAMP | NULL | Non-NULL = revoked |
| `expires_at` | TIMESTAMP | NULL | Optional expiry |

- **Why SHA-256 and not Argon2id**: the token is 32 CSPRNG bytes — 256 bits — so it is
  already beyond brute force and a slow KDF adds nothing. It would instead add latency to
  every authenticated call and create a CPU-exhaustion vector, since an attacker spraying
  invalid tokens would force expensive hashes. The admin *password* is the opposite case
  and does use Argon2id. See [research.md](./research.md) §2.
- **Wire format**: `qur_<base64url of 32 random bytes>`, shown exactly once (FR-033).
- **`last_used_at` write policy**: updated at most once per minute per token. Writing it on
  every request would put a database write on the hot path of every API call — and on
  SQLite, contend for the single writer lock.

## Entity: `codes`

The central entity. One row per dynamic QR code.

| Column | Type | Constraints | Notes |
|--------|------|-------------|-------|
| `id` | TEXT | PK | `cod_` + 16 random base32 chars |
| `short_code` | TEXT | NOT NULL, UNIQUE (case-insensitive) | The `/r/{short_code}` value |
| `is_alias` | BOOL | NOT NULL | true if user-chosen, false if generated |
| `user_id` | TEXT | NOT NULL, FK → `users.id` | |
| `destination` | TEXT | NOT NULL | Current target URL |
| `state` | TEXT | NOT NULL | `active` \| `disabled` \| `deleted` |
| `styling_id` | TEXT | NOT NULL, FK → `styling_profiles.id` | |
| `blob_key` | TEXT | NOT NULL | Key of the rendered image in the blob store |
| `blob_etag` | TEXT | NOT NULL | Serves conditional requests without a blob round-trip |
| `version` | INTEGER | NOT NULL, default 1 | Incremented on every mutation; the `If-Match` value |
| `created_at` | TIMESTAMP | NOT NULL | |
| `updated_at` | TIMESTAMP | NOT NULL | |
| `deleted_at` | TIMESTAMP | NULL | Soft delete; the row and its alias survive |

**Indexes**

- `UNIQUE (short_code)` case-insensitive — the resolution index; every scan uses it.
  Dialect: `COLLATE NOCASE` vs `UNIQUE INDEX ON (lower(short_code))`.
- `(user_id, created_at DESC)` — the owner's paginated listing (FR-016).
- `(user_id, destination)` — destination filtering (FR-016).

**State transitions**

```
              create
                │
                ▼
          ┌──────────┐  disable   ┌───────────┐
          │  active  │ ─────────▶ │ disabled  │
          │          │ ◀───────── │           │
          └────┬─────┘   enable   └─────┬─────┘
               │  delete                │ delete
               └───────────┬────────────┘
                           ▼
                     ┌───────────┐
                     │  deleted  │   terminal for the code;
                     └───────────┘   short_code stays reserved
```

`deleted` is terminal. The row is retained rather than removed because its `short_code`
must stay reserved (FR-018) — see `alias_reservations`.

**Validation**

- `destination` scheme must be on the configured allow-list, checked on create **and on
  every update** (FR-011). Default: `http`, `https` only.
- `destination` must not resolve to this instance's own `/r/` path (FR-012), checked after
  URL normalisation so `//host/r/x` and percent-encoded forms are caught too.
- `short_code` generated form: 12 chars, lowercase Crockford base32.
- `short_code` alias form: 3–64 chars `[a-z0-9-]`, alphanumeric first and last, no
  consecutive hyphens, not on the reserved list, and **must not match the generated
  shape** `^[0-9a-hjkmnp-tv-z]{12}$`.
- `short_code` is immutable after creation. There is no update path for it, by design: a
  mutable short code would let a printed code be repointed by reassignment, defeating the
  destination-allow-list check entirely.
- **Optimistic concurrency uses `version`, not `updated_at`.** Every mutation runs
  `UPDATE ... SET version = version + 1 WHERE id = ? AND version = ?`; zero rows affected
  means the caller lost the race and receives `ErrConflict`. A timestamp cannot do this
  job — HTTP-date has one-second granularity, and two edits inside the same second would
  both pass — and a compare-and-increment on an integer is dialect-neutral.

## Entity: `alias_reservations`

| Column | Type | Constraints | Notes |
|--------|------|-------------|-------|
| `short_code` | TEXT | PK (case-insensitive) | |
| `code_id` | TEXT | NULL, FK → `codes.id` | NULL once the code is deleted |
| `reserved_at` | TIMESTAMP | NOT NULL | |
| `released_at` | TIMESTAMP | NULL | Non-NULL = an admin explicitly freed it |

**Why this table exists at all.** It looks redundant against `codes.short_code`, and it is
not. When a code is deleted, its short code must remain unavailable (FR-018). Keeping that
guarantee in `codes` alone would mean deleted rows can never be purged, coupling data
retention to a security property. Separating the reservation lets code rows be purged on
an operator's schedule while the reservation — a single short row — persists.

The risk it closes is concrete: a campaign prints 5,000 flyers pointing at
`/r/spring-sale`, the campaign ends, the code is deleted. Without a reservation, anyone can
register `spring-sale` and inherit every flyer already in circulation. Because printed
artefacts cannot be recalled, alias reuse is a permanent redirect handover to whoever asks
second — not a convenience question.

## Entity: `styling_profiles`

| Column | Type | Constraints | Notes |
|--------|------|-------------|-------|
| `id` | TEXT | PK | |
| `fg_color` | TEXT | NOT NULL | `#RRGGBB` |
| `bg_color` | TEXT | NOT NULL | `#RRGGBB` |
| `module_shape` | TEXT | NOT NULL | `square` \| `dot` \| `rounded` |
| `margin_modules` | INTEGER | NOT NULL | Quiet zone, ≥ 4 per ISO |
| `size_px` | INTEGER | NOT NULL | Bounded by config (FR-029) |
| `ec_level` | TEXT | NOT NULL | `L` \| `M` \| `Q` \| `H` — as *requested* |
| `ec_level_effective` | TEXT | NOT NULL | After automatic raising for a logo (FR-027) |
| `logo_blob_key` | TEXT | NULL | |
| `logo_scale` | REAL | NULL | Fraction of module area |

- **Why two EC columns.** FR-026 lets the user request a level; FR-027 raises it when a
  logo demands more recovery. Storing only the effective value would make the UI show a
  level the user never chose with no way to distinguish "you asked for H" from "we raised
  you to H". Storing only the requested value would lose what was actually encoded.
- **Validation**: `logo_scale` ≤ the per-level budget — L 5%, M 12%, Q 20%, H 25%; contrast
  between `fg_color` and `bg_color` ≥ 3:1 hard floor, 4.5:1 default gate (FR-028).
- Profiles are immutable once attached to a code. Restyling creates a new code, because the
  printed image cannot change (spec Assumptions).

## Entity: `scan_events`

Raw, individually retained scans. The highest-volume table by far.

| Column | Type | Constraints | Notes |
|--------|------|-------------|-------|
| `id` | INTEGER | PK, autoincrement | Internal only, never exposed |
| `code_id` | TEXT | NOT NULL, FK → `codes.id` | |
| `occurred_at` | TIMESTAMP | NOT NULL | |
| `ua_family` | TEXT | NOT NULL | Browser name |
| `device_category` | TEXT | NOT NULL | `desktop`\|`mobile`\|`tablet`\|`tv`\|`bot` |
| `referrer_host` | TEXT | NOT NULL | **Host only**, never the full referrer URL |
| `is_bot` | BOOL | NOT NULL | Tagged, never dropped |

- **Dialect**: identity column — `INTEGER PRIMARY KEY AUTOINCREMENT` vs
  `GENERATED ALWAYS AS IDENTITY`. This is the irreducible divergence that forced the
  constitution amendment.
- **Index**: `(code_id, occurred_at)` — serves both range queries and chunked retention.
- **Referrer is stored as host only.** A full referrer URL routinely carries query strings
  with session tokens, email addresses, and campaign identifiers belonging to a third-party
  site. Truncating to the host at write time means that data never enters the database at
  all, rather than relying on it being scrubbed correctly on the way out.
- **Bots are tagged, not dropped** (FR-019 with research §4): dropping them would break the
  sum-equals-total invariant below, and any maintained bot list rots.

## Entity: `scan_rollups`

Aggregates that outlive raw events (FR-024).

| Column | Type | Constraints | Notes |
|--------|------|-------------|-------|
| `code_id` | TEXT | NOT NULL, FK → `codes.id` | |
| `hour_bucket` | TIMESTAMP | NOT NULL | Truncated to the hour, UTC |
| `dimension` | TEXT | NOT NULL | `total`\|`ua_family`\|`device_category`\|`referrer_host`\|`is_bot` |
| `value` | TEXT | NOT NULL | `''` when `dimension = 'total'` |
| `count` | INTEGER | NOT NULL | |

- **PK**: `(code_id, hour_bucket, dimension, value)`.
- **Written in the same transaction as the raw insert**, upserted with
  `ON CONFLICT ... DO UPDATE SET count = count + ?` — syntax verified identical on both
  backends.
- **Why the same transaction.** This makes FR-023's "every dimension breakdown totals to
  the overall count" true *by construction*. A separate periodic rollup job would create a
  window in which totals and breakdowns disagree, plus reconciliation logic that has to
  stay correct forever. Paying one upsert per dimension per batch buys away an entire class
  of bug.
- Retained indefinitely; decoupled from raw-event retention.

## Entity: `sessions` (optional, revocation support)

| Column | Type | Constraints | Notes |
|--------|------|-------------|-------|
| `id` | TEXT | PK | Matches the JWT `jti` |
| `user_id` | TEXT | NOT NULL, FK → `users.id` | |
| `issued_at` | TIMESTAMP | NOT NULL | |
| `expires_at` | TIMESTAMP | NOT NULL | |
| `revoked_at` | TIMESTAMP | NULL | |

Bulk invalidation uses `users.token_version` rather than deleting rows here, so signing out
everywhere is one integer bump instead of a table scan. Expired rows are hard-deleted by
the retention job.

## Entity: instance configuration

Not persisted. Read from the environment at startup per FR-047, never writable through any
interface, and never returned by any endpoint (FR-049).

---

## Relationships

```
users ──┬──< api_tokens
        ├──< sessions
        └──< codes ──┬──< scan_events
                     ├──< scan_rollups
                     ├──── styling_profiles  (1:1, immutable)
                     └──── alias_reservations (1:1, survives code deletion)
```

## Retention

| Data | Default | Mechanism |
|------|---------|-----------|
| `scan_events` | 365 days | Daily chunked delete, 1,000 rows per statement via subquery — SQLite has no `DELETE ... LIMIT` without a compile flag |
| `scan_rollups` | Indefinite | Never pruned; this is what makes long-range trends survive |
| `sessions` | On expiry | Hard delete |
| `codes` (deleted) | Operator choice | Purgeable; `alias_reservations` retains the reservation |
| `alias_reservations` | Indefinite | Released only by explicit admin action |
