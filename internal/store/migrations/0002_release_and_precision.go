package migrations

import (
	"context"
	"database/sql"
)

// up0002 makes the short-code uniqueness index PARTIAL and widens logo_scale on Postgres.
//
// Why partial: 0001 made codes_short_code_ci a full unique index, so a soft-deleted row
// kept blocking its own short code forever. Both drivers worked around that by renaming
// the deleted row's short_code to `released:<code>:<id>` on ReleaseAlias — mangling
// history to satisfy an index. With the index restricted to live rows (state <> 'deleted')
// a deleted row and a live row may share a short code, and the namespace is instead
// enforced where data-model.md puts it: an alias_reservations row with
// released_at IS NULL means taken. Rows already renamed by the old drivers are left as
// they are; their reservations were deleted at release time, so nothing references them.
//
// Why widen: styling_profiles.logo_scale was declared REAL, which Postgres stores as
// float4 (the PG driver used to round to 6 decimals on read to hide it). DOUBLE PRECISION
// round-trips a float64 exactly. SQLite's REAL is already an 8-byte IEEE float, so the
// SQLite branch has nothing to change and its column is left alone.
func up0002(ctx context.Context, tx *sql.Tx, d Dialect) error {
	switch d {
	case SQLite:
		return exec(ctx, tx,
			`DROP INDEX codes_short_code_ci`,
			`CREATE UNIQUE INDEX codes_short_code_ci ON codes(short_code COLLATE NOCASE) WHERE state <> 'deleted'`,
			// The redirect path looks up deleted rows too (fallback destination), which a
			// partial index cannot serve; keep a plain index so that lookup is never a scan.
			`CREATE INDEX codes_short_code_lookup ON codes(short_code COLLATE NOCASE)`,
		)
	case Postgres:
		return exec(ctx, tx,
			`DROP INDEX codes_short_code_ci`,
			`CREATE UNIQUE INDEX codes_short_code_ci ON codes(lower(short_code)) WHERE state <> 'deleted'`,
			`CREATE INDEX codes_short_code_lookup ON codes(lower(short_code))`,
			`ALTER TABLE styling_profiles ALTER COLUMN logo_scale TYPE DOUBLE PRECISION`,
		)
	}
	return nil
}
