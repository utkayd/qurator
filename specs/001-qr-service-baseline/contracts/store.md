# Interface Contract: `Store` and `BlobStore`

**Feature**: [../spec.md](../spec.md) | **Plan**: [../plan.md](../plan.md)

These are the two interfaces Constitution Principle II governs. Both are implemented twice
and both are proven interchangeable by one contract suite that every driver must pass
**unmodified**. A driver that needs the suite changed to pass is a driver that has changed
behaviour, which is precisely what the suite exists to prevent.

## Sentinel errors

Declared in `internal/store` and `internal/blob`. Drivers translate their native errors
into these; no backend error type escapes a driver.

```go
var (
    ErrNotFound      = errors.New("store: not found")
    ErrConflict      = errors.New("store: conflict")       // optimistic-concurrency loss
    ErrAliasTaken    = errors.New("store: alias taken")     // includes reserved aliases
    ErrBlobNotFound  = errors.New("blob: not found")
)
```

**Driver translation, verified against live databases in Phase 0:**

| Driver | Native signal | Maps to |
|--------|--------------|---------|
| SQLite | extended result code `2067` (`SQLITE_CONSTRAINT_UNIQUE`) via `modernc.org/sqlite/lib` | `ErrAliasTaken` on the `short_code` index, else `ErrConflict` |
| SQLite | `sql.ErrNoRows` | `ErrNotFound` |
| PostgreSQL | SQLSTATE `23505` via `pgconn.PgError`, extracted with `errors.As` | as above |
| PostgreSQL | `sql.ErrNoRows` | `ErrNotFound` |
| S3 | `minio.ErrorResponse{Code: "NoSuchKey"}` | `ErrBlobNotFound` |
| Filesystem | `errors.Is(err, fs.ErrNotExist)` | `ErrBlobNotFound` |

Callers use `errors.Is`. Drivers wrap with `%w` so the underlying cause stays available for
logging without leaking into control flow.

## `Store`

```go
type Store interface {
    // Users
    CreateUser(ctx context.Context, u *domain.User) error
    GetUserByID(ctx context.Context, id string) (*domain.User, error)
    GetUserByEmail(ctx context.Context, email string) (*domain.User, error) // case-insensitive
    BumpTokenVersion(ctx context.Context, userID string) (int64, error)
    CountUsers(ctx context.Context) (int64, error)                          // bootstrap guard

    // API tokens
    CreateToken(ctx context.Context, t *domain.APIToken) error
    GetTokenByID(ctx context.Context, id string) (*domain.APIToken, error)
    ListTokens(ctx context.Context, userID string) ([]*domain.APIToken, error)
    RevokeToken(ctx context.Context, id, userID string) error
    TouchTokenLastUsed(ctx context.Context, id string, at time.Time) error

    // Codes
    CreateCode(ctx context.Context, c *domain.Code) error // persists c.Styling and reserves the short code atomically
    GetCodeByShortCode(ctx context.Context, shortCode string) (*domain.Code, error)
    GetCodeByID(ctx context.Context, id, userID string) (*domain.Code, error)
    ListCodes(ctx context.Context, f domain.CodeFilter) ([]*domain.Code, string, error)
    UpdateDestination(ctx context.Context, id, userID, dest string, expectedVersion int64) error
    SetCodeState(ctx context.Context, id, userID string, state domain.CodeState) error
    DeleteCode(ctx context.Context, id, userID string) error

    // Aliases (reservation happens inside CreateCode; there is no separate ReserveAlias)
    IsAliasAvailable(ctx context.Context, shortCode string) (bool, error)
    ReleaseAlias(ctx context.Context, shortCode string) error // admin only; ErrConflict if the owning code is not deleted

    // Analytics
    InsertScanBatch(ctx context.Context, b domain.ScanBatch) error // events + rollup deltas, one transaction
    QueryAnalytics(ctx context.Context, q domain.AnalyticsQuery) (*domain.AnalyticsResult, error)
    PruneScanEvents(ctx context.Context, before time.Time, limit int) (int64, error)

    // Lifecycle
    Migrate(ctx context.Context) error
    Ping(ctx context.Context) error
    Close() error
}
```

### Contract requirements the suite MUST verify

Each of these is a behaviour that could plausibly differ between backends, which is exactly
why it is pinned:

1. **Case-insensitive short-code uniqueness.** Creating `Spring-Sale` after `spring-sale`
   returns `ErrAliasTaken`. Verified on both dialects, which reach this result by different
   mechanisms (`COLLATE NOCASE` vs a `lower()` index).
2. **Case-insensitive lookup.** `GetCodeByShortCode("SPRING-SALE")` finds `spring-sale`.
3. **Reserved alias survives deletion.** Delete a code, then attempt to create another with
   the same short code — `ErrAliasTaken`. This is FR-018's security property and the single
   most important item in the suite.
4. **`ErrNotFound` on a missing row**, never a nil result with a nil error.
5. **Ownership isolation.** `GetCodeByID` with a non-owner `userID` returns `ErrNotFound`,
   not the row and not `ErrForbidden` — an existence oracle would let one user enumerate
   another's codes.
6. **Optimistic concurrency.** Two `UpdateDestination` calls with the same
   `expectedVersion` — exactly one succeeds and the row's `version` is now
   `expectedVersion + 1`; the other returns `ErrConflict`. This is the spec's concurrent-edit
   edge case; without it the losing write vanishes silently. The suite issues both calls
   within the same second deliberately, so a driver that cheats with a timestamp fails.
7. **`InsertScanBatch` is atomic** — a batch containing one bad row inserts nothing.
8. **Rollups equal raw counts.** After inserting a known batch, every dimension breakdown
   sums to the total for the same range. This is FR-023's invariant, asserted directly.
9. **Timestamps round-trip in UTC** with no truncation surprises, on both dialects.
10. **`PruneScanEvents` respects `limit`** and never removes rollups.
11. **`Migrate` is idempotent** — running it twice is a no-op, and running it against a
    populated database preserves data.
12. **Pagination is stable** under concurrent inserts: a cursor does not skip or duplicate
    rows when new codes are created mid-listing.

### Semantics pinned by the suite (driver authors: these are not optional)

The reference in-memory implementation and the contract suite fix the following, so every
driver must match them exactly:

- `ListCodes` returns newest first (`created_at DESC, id DESC`), excludes `deleted` codes,
  and filters `Destination` as a case-insensitive substring. `CreatedAfter`/`CreatedBefore`
  boundary inclusivity is deliberately unpinned — tests never place a row on the boundary.
- `GetCodeByID` and `GetCodeByShortCode` DO return `deleted` rows: the redirect path needs
  to distinguish "deleted" from "never existed" to pick the landing response.
- `CreateCode` stores `short_code` lowercased and sets `Version = 1` regardless of input.
- `SetCodeState` on a `deleted` code returns `ErrConflict` — deleted is terminal.
- `ReleaseAlias` is not idempotent: a second call returns `ErrNotFound` (operators running
  it by hand learn whether it did anything). Live code → `ErrConflict`.
- `IsAliasAvailable` covers store-level reservations only; the reserved-word list is
  enforced by `internal/shortcode` before the store is consulted.
- `QueryAnalytics` covers `[From, To)`, aligned to hour buckets (weeks start Monday UTC),
  emits only non-empty series points, and always returns a non-nil `Breakdowns` map with
  the four non-total dimensions present.
- Timestamps round-trip at microsecond precision in `time.UTC`.
- `storetest.BuildRollups(events)` is the canonical rollup computation; the analytics
  stream must use it rather than reimplementing it, or breakdowns and totals can diverge.

### Harness shape

```go
// internal/store/storetest/contract.go
func RunStoreContract(t *testing.T, newStore func(t *testing.T) store.Store)
```

Each driver has a thin wrapper:

```go
// internal/store/postgres/postgres_test.go
func TestPostgresStore(t *testing.T) {
    dsn := os.Getenv("QURATOR_TEST_PG_DSN")
    if dsn == "" {
        t.Skip("QURATOR_TEST_PG_DSN not set; skipping PostgreSQL contract tests")
    }
    storetest.RunStoreContract(t, func(t *testing.T) store.Store { /* ... */ })
}
```

**Why skip-on-missing-DSN rather than testcontainers.** A contributor running
`go test ./...` with no Docker gets a green, honest run with skips clearly reported.
Requiring Docker to participate at all would contradict the project's low-barrier
self-hosting ethos, and it is the kind of friction that quietly costs contributions. CI
supplies the environment variables via service containers, so coverage is not reduced —
only the local prerequisite is.

## `BlobStore`

```go
type BlobStore interface {
    Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (etag string, err error)
    Get(ctx context.Context, key string) (io.ReadCloser, *BlobInfo, error)
    Stat(ctx context.Context, key string) (*BlobInfo, error)
    Delete(ctx context.Context, key string) error
    Ping(ctx context.Context) error
}

type BlobInfo struct {
    Key         string
    Size        int64
    ContentType string
    ETag        string
    ModTime     time.Time
}
```

### Contract requirements

1. **Round-trip fidelity** — bytes out equal bytes in, for content including NUL bytes.
2. **`ETag` is stable** across `Put`, `Get`, and `Stat` for the same content, and differs
   for different content. The scan path serves conditional requests from the stored ETag,
   so an unstable one causes silent cache misses at exactly our hottest endpoint.
3. **`ErrBlobNotFound`** for a missing key from `Get`, `Stat`, and `Delete`.
4. **`Delete` is idempotent** — deleting a missing key returns `ErrBlobNotFound`, never a
   different error class per backend.
5. **Overwrite semantics** — `Put` to an existing key replaces it and returns a new ETag.
6. **Key safety** — keys containing `../`, absolute paths, or NUL are rejected by both
   drivers. On the filesystem driver this is a path-traversal defence; the test asserts it
   for S3 too so the two behave identically.
7. **Concurrent `Put` to one key** leaves complete, uncorrupted content — never a partial
   file. On the filesystem driver this is what the temp-file-and-rename sequence buys.

### Filesystem driver durability

Write a temp file in the **same directory** → `fsync` the file → `rename` →
**`fsync` the parent directory**.

That final directory `fsync` is the commonly omitted step. Without it the rename itself may
not survive a crash, so a blob can be reported written and then be absent after a power
loss — a failure that only appears under exactly the conditions where it hurts most.
