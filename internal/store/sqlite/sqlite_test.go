package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/utkayd/qurator/internal/store"
	"github.com/utkayd/qurator/internal/store/storetest"
)

// TestSQLiteStore runs the shared contract suite against a temp-file database opened
// through the registry, so the test also proves the driver registered itself.
func TestSQLiteStore(t *testing.T) {
	storetest.RunStoreContract(t, func(t *testing.T) store.Store {
		t.Helper()
		s, err := store.Open(t.Context(), "sqlite", filepath.Join(t.TempDir(), "qurator.db"))
		if err != nil {
			t.Fatalf("open sqlite: %v", err)
		}
		if err := s.Migrate(t.Context()); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}
