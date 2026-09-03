package migrations

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

func TestApplySQLiteIdempotent(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/m.db?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := Apply(ctx, db, SQLite); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email,is_admin,token_version,source,created_at) VALUES('usr_1','A@x.com',0,0,'local','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, db, SQLite); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("data lost across re-apply: n=%d err=%v", n, err)
	}
	// case-insensitive uniqueness is enforced by the index, not the app
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email,is_admin,token_version,source,created_at) VALUES('usr_2','a@X.COM',0,0,'local','2026-01-01T00:00:00Z')`); err == nil {
		t.Fatal("expected case-insensitive unique violation on users.email")
	}
}

func TestApplyPostgres(t *testing.T) {
	dsn := os.Getenv("QURATOR_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("QURATOR_TEST_PG_DSN not set; skipping PostgreSQL migration test")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DROP SCHEMA IF EXISTS mig_test CASCADE; CREATE SCHEMA mig_test; SET search_path TO mig_test`); err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, db, Postgres); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := Apply(ctx, db, Postgres); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
}
