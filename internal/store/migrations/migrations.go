// Package migrations holds ONE ordered migration sequence applied to both SQL backends
// (Constitution v1.0.1, Principle II). goose does not translate SQL across dialects, so
// each migration is a Go function that branches its DDL per dialect internally. Two
// independently numbered sets are prohibited; add new versions here only.
package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

// Dialect selects the DDL branch inside each migration.
type Dialect string

const (
	SQLite   Dialect = "sqlite"
	Postgres Dialect = "postgres"
)

// migration is one version with a per-dialect body.
type migration struct {
	version int64
	up      func(ctx context.Context, tx *sql.Tx, d Dialect) error
}

// all is the ordered sequence. Append only; never renumber.
var all = []migration{
	{version: 1, up: up0001},
	{version: 2, up: up0002},
}

// Apply runs every pending migration against db for the given dialect. It is idempotent.
func Apply(ctx context.Context, db *sql.DB, d Dialect) error {
	var gd goose.Dialect
	switch d {
	case SQLite:
		gd = goose.DialectSQLite3
	case Postgres:
		gd = goose.DialectPostgres
	default:
		return fmt.Errorf("migrations: unknown dialect %q", d)
	}
	gms := make([]*goose.Migration, 0, len(all))
	for _, m := range all {
		m := m
		up := &goose.GoFunc{RunTx: func(ctx context.Context, tx *sql.Tx) error { return m.up(ctx, tx, d) }}
		gms = append(gms, goose.NewGoMigration(m.version, up, nil))
	}
	p, err := goose.NewProvider(gd, db, nil,
		goose.WithGoMigrations(gms...),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return fmt.Errorf("migrations: provider: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrations: up: %w", err)
	}
	return nil
}

// exec runs each statement in order inside the transaction.
func exec(ctx context.Context, tx *sql.Tx, stmts ...string) error {
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("%w\nstatement: %s", err, s)
		}
	}
	return nil
}
