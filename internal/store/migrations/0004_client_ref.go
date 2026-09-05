package migrations

import (
	"context"
	"database/sql"
)

// up0004 adds codes.client_ref (spec 003, FR-206): the caller's optional idempotency key.
//
// The column is nullable — most codes never get one — and uniqueness is per user, so
// the index is a PARTIAL unique index over (user_id, client_ref) restricted to rows that
// carry a ref. Both dialects support the WHERE clause on CREATE INDEX (SQLite since
// 3.8.0), so the DDL is written once. NULLs would not collide in a plain unique index
// either, but the partial index keeps the index itself tiny on instances that never use
// the feature. Its name, codes_user_client_ref, is what the drivers match when
// translating a unique violation into store.ErrClientRefTaken.
func up0004(ctx context.Context, tx *sql.Tx, d Dialect) error {
	switch d {
	case SQLite, Postgres:
		return exec(ctx, tx,
			`ALTER TABLE codes ADD COLUMN client_ref TEXT NULL`,
			`CREATE UNIQUE INDEX codes_user_client_ref ON codes(user_id, client_ref) WHERE client_ref IS NOT NULL`,
		)
	}
	return nil
}
