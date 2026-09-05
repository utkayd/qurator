package migrations

import (
	"context"
	"database/sql"
)

// up0003 adds codes.mode (spec 002, FR-101/FR-108).
//
// The column is NOT NULL with a constant default of 'dynamic', which both dialects apply
// to every existing row as part of the ADD COLUMN itself — so every code created before
// this feature is recorded as dynamic explicitly, not inferred from a NULL. SQLite
// supports ADD COLUMN with a constant default without a table rebuild; Postgres 11+
// stores the default in the catalogue and rewrites nothing. The value set is enforced by
// the service (domain.CodeMode), not a CHECK constraint, so a future mode is a code
// change and not another migration.
func up0003(ctx context.Context, tx *sql.Tx, d Dialect) error {
	switch d {
	case SQLite, Postgres:
		return exec(ctx, tx, `ALTER TABLE codes ADD COLUMN mode TEXT NOT NULL DEFAULT 'dynamic'`)
	}
	return nil
}
