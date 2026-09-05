package migrations

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

func TestApplySQLiteIdempotent(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/m.db?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
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

// TestSQLitePartialShortCodeIndex pins migration 0002: a deleted code may share its short
// code (case-insensitively) with one live code, but two live codes never may.
func TestSQLitePartialShortCodeIndex(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/m.db?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	if err := Apply(ctx, db, SQLite); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT max(version_id) FROM goose_db_version`).Scan(&version); err != nil || version < 2 {
		t.Fatalf("schema version = %d (err %v), want >= 2", version, err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO users(id,email,is_admin,token_version,source,created_at) VALUES('usr_1','a@x.com',0,0,'local','2026-01-01T00:00:00Z')`)
	mustExec(`INSERT INTO styling_profiles(id,fg_color,bg_color,module_shape,margin_modules,size_px,ec_level,ec_level_effective) VALUES('sty_1','#000','#fff','square',4,256,'M','M')`)
	insertCode := func(id, shortCode, state string) error {
		_, err := db.ExecContext(ctx, `INSERT INTO codes(id,short_code,is_alias,user_id,destination,state,styling_id,blob_key,blob_etag,version,created_at,updated_at)
			VALUES(?,?,1,'usr_1','https://x','`+state+`','sty_1','k','e',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, id, shortCode)
		return err
	}
	if err := insertCode("cod_1", "spring-sale", "deleted"); err != nil {
		t.Fatalf("deleted row: %v", err)
	}
	if err := insertCode("cod_2", "Spring-Sale", "active"); err != nil {
		t.Fatalf("live row next to a deleted one with the same short code must be allowed after 0002: %v", err)
	}
	if err := insertCode("cod_3", "SPRING-SALE", "disabled"); err == nil {
		t.Fatal("second live row with the same short code (any case) must violate codes_short_code_ci")
	}
	if err := insertCode("cod_4", "spring-sale", "deleted"); err != nil {
		t.Fatalf("a second deleted row with the same short code must be allowed: %v", err)
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
	defer func() { _ = db.Close() }()
	// One connection, so the session-level search_path below applies to every statement.
	db.SetMaxOpenConns(1)
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

	// 0002: logo_scale is double precision and the short-code index is partial.
	var typ string
	if err := db.QueryRowContext(ctx, `SELECT data_type FROM information_schema.columns
		WHERE table_schema = 'mig_test' AND table_name = 'styling_profiles' AND column_name = 'logo_scale'`).Scan(&typ); err != nil {
		t.Fatal(err)
	}
	if typ != "double precision" {
		t.Fatalf("styling_profiles.logo_scale type = %q, want double precision", typ)
	}
	var def string
	if err := db.QueryRowContext(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname = 'mig_test' AND indexname = 'codes_short_code_ci'`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(def, "WHERE") || !strings.Contains(def, "deleted") {
		t.Fatalf("codes_short_code_ci is not partial: %s", def)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO users(id,email,is_admin,token_version,source,created_at) VALUES('usr_1','a@x.com',false,0,'local',now())`)
	mustExec(`INSERT INTO styling_profiles(id,fg_color,bg_color,module_shape,margin_modules,size_px,ec_level,ec_level_effective,logo_scale) VALUES('sty_1','#000','#fff','square',4,256,'M','M',0.2)`)
	var scale float64
	if err := db.QueryRowContext(ctx, `SELECT logo_scale FROM styling_profiles WHERE id = 'sty_1'`).Scan(&scale); err != nil || scale != 0.2 {
		t.Fatalf("logo_scale round trip = %v (err %v), want exactly 0.2", scale, err)
	}
	insertCode := func(id, shortCode, state string) error {
		_, err := db.ExecContext(ctx, `INSERT INTO codes(id,short_code,is_alias,user_id,destination,state,styling_id,blob_key,blob_etag,version,created_at,updated_at)
			VALUES($1,$2,true,'usr_1','https://x',$3,'sty_1','k','e',1,now(),now())`, id, shortCode, state)
		return err
	}
	if err := insertCode("cod_1", "spring-sale", "deleted"); err != nil {
		t.Fatalf("deleted row: %v", err)
	}
	if err := insertCode("cod_2", "Spring-Sale", "active"); err != nil {
		t.Fatalf("live row next to a deleted one with the same short code must be allowed after 0002: %v", err)
	}
	if err := insertCode("cod_3", "SPRING-SALE", "disabled"); err == nil {
		t.Fatal("second live row with the same short code (any case) must violate codes_short_code_ci")
	}

	// 0003: codes.mode exists, is NOT NULL, and defaults to 'dynamic' (FR-108).
	var nullable, modeDefault string
	if err := db.QueryRowContext(ctx, `SELECT is_nullable, column_default FROM information_schema.columns
		WHERE table_schema = 'mig_test' AND table_name = 'codes' AND column_name = 'mode'`).Scan(&nullable, &modeDefault); err != nil {
		t.Fatalf("codes.mode column: %v", err)
	}
	if nullable != "NO" || !strings.Contains(modeDefault, "dynamic") {
		t.Fatalf("codes.mode nullable=%q default=%q, want NOT NULL DEFAULT 'dynamic'", nullable, modeDefault)
	}
	var mode string
	if err := db.QueryRowContext(ctx, `SELECT mode FROM codes WHERE id = 'cod_2'`).Scan(&mode); err != nil || mode != "dynamic" {
		t.Fatalf("row inserted without mode reads %q (err %v), want dynamic", mode, err)
	}
}

// TestSQLiteModeBackfill pins migration 0003 (spec 002, FR-108): a code row that
// existed before the mode column reads back as 'dynamic' — explicitly, via the
// column's NOT NULL default — and a direct row round-trips.
func TestSQLiteModeBackfill(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/m.db?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	// Apply only 0001 and 0002, insert a pre-feature row, then bring the schema to 0003.
	if err := applyUpTo(ctx, db, SQLite, 2); err != nil {
		t.Fatalf("apply up to 0002: %v", err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO users(id,email,is_admin,token_version,source,created_at) VALUES('usr_1','a@x.com',0,0,'local','2026-01-01T00:00:00Z')`)
	mustExec(`INSERT INTO styling_profiles(id,fg_color,bg_color,module_shape,margin_modules,size_px,ec_level,ec_level_effective) VALUES('sty_1','#000','#fff','square',4,256,'M','M')`)
	mustExec(`INSERT INTO codes(id,short_code,is_alias,user_id,destination,state,styling_id,blob_key,blob_etag,version,created_at,updated_at)
		VALUES('cod_old','old-code',1,'usr_1','https://x','active','sty_1','k','e',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	var hasMode int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('codes') WHERE name = 'mode'`).Scan(&hasMode); err != nil || hasMode != 0 {
		t.Fatalf("codes.mode must not exist before 0003: n=%d err=%v", hasMode, err)
	}

	if err := Apply(ctx, db, SQLite); err != nil {
		t.Fatalf("apply 0003: %v", err)
	}
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT max(version_id) FROM goose_db_version`).Scan(&version); err != nil || version < 3 {
		t.Fatalf("schema version = %d (err %v), want >= 3", version, err)
	}
	var mode string
	if err := db.QueryRowContext(ctx, `SELECT mode FROM codes WHERE id = 'cod_old'`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "dynamic" {
		t.Fatalf("pre-0003 row mode = %q, want dynamic", mode)
	}
	// New rows: the default still applies when mode is omitted, and direct persists.
	mustExec(`INSERT INTO codes(id,short_code,is_alias,user_id,destination,state,styling_id,blob_key,blob_etag,version,created_at,updated_at)
		VALUES('cod_new','new-code',1,'usr_1','https://x','active','sty_1','k','e',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	mustExec(`INSERT INTO codes(id,short_code,is_alias,user_id,mode,destination,state,styling_id,blob_key,blob_etag,version,created_at,updated_at)
		VALUES('cod_direct','direct-code',1,'usr_1','direct','https://x','active','sty_1','k','e',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	for id, want := range map[string]string{"cod_new": "dynamic", "cod_direct": "direct"} {
		if err := db.QueryRowContext(ctx, `SELECT mode FROM codes WHERE id = ?`, id).Scan(&mode); err != nil || mode != want {
			t.Fatalf("%s mode = %q (err %v), want %q", id, mode, err, want)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO codes(id,short_code,is_alias,user_id,mode,destination,state,styling_id,blob_key,blob_etag,version,created_at,updated_at)
		VALUES('cod_null','null-code',1,'usr_1',NULL,'https://x','active','sty_1','k','e',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err == nil {
		t.Fatal("NULL mode must be rejected: the column is NOT NULL so 'unknown mode' cannot exist")
	}
}
