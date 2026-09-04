package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/utkayd/qurator/internal/store"
	"github.com/utkayd/qurator/internal/store/storetest"
)

// TestPostgresStore runs the contract suite against a live PostgreSQL. Every newStore
// call gets its own schema (CREATE SCHEMA test_<rand> + search_path) so parallel CI jobs
// sharing one server never collide, and the schema is dropped afterwards.
func TestPostgresStore(t *testing.T) {
	dsn := os.Getenv("QURATOR_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("QURATOR_TEST_PG_DSN not set; skipping PostgreSQL contract tests")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if err := admin.PingContext(context.Background()); err != nil {
		t.Fatalf("ping %s: %v", dsn, err)
	}

	storetest.RunStoreContract(t, func(t *testing.T) store.Store {
		t.Helper()
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			t.Fatal(err)
		}
		schema := "test_" + hex.EncodeToString(b[:])
		if _, err := admin.ExecContext(t.Context(), "CREATE SCHEMA "+schema); err != nil {
			t.Fatalf("create schema: %v", err)
		}
		t.Cleanup(func() {
			_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		})
		s, err := store.Open(t.Context(), "postgres", withSearchPath(dsn, schema))
		if err != nil {
			t.Fatalf("open postgres: %v", err)
		}
		if err := s.Migrate(t.Context()); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

// withSearchPath appends a search_path runtime parameter to either DSN form pgx accepts.
func withSearchPath(dsn, schema string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		return dsn + sep + "search_path=" + schema
	}
	return dsn + " search_path=" + schema
}
