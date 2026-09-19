package sqlite_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/utkayd/qurator/internal/store"
)

// F14: the database holds password hashes, token hashes and every destination. It must
// be created 0600 even when the data directory is a pre-existing, world-readable
// bind mount and the process umask is permissive.
func TestSQLiteDatabaseFileIsPrivate(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "qurator.db")
	s, err := store.Open(t.Context(), "sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("database file mode = %04o, want 0600", got)
	}
	// The WAL and shared-memory companions inherit the database file's mode.
	companions, _ := filepath.Glob(path + "-*")
	if len(companions) == 0 {
		t.Fatal("expected WAL companion files after migrate")
	}
	for _, c := range companions {
		fi, err := os.Stat(c)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s mode = %04o, want 0600", filepath.Base(c), got)
		}
	}
	// A pre-existing permissive file is tightened too.
	loose := filepath.Join(dir, "loose.db")
	if err := os.WriteFile(loose, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s2, err := store.Open(t.Context(), "sqlite", loose)
	if err != nil {
		t.Fatalf("open loose: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	fi, _ = os.Stat(loose)
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("pre-existing database file mode = %04o, want 0600", got)
	}
	// The parent directory the store creates itself is private as well.
	nested := filepath.Join(dir, "sub", "qurator.db")
	s3, err := store.Open(t.Context(), "sqlite", nested)
	if err != nil {
		t.Fatalf("open nested: %v", err)
	}
	t.Cleanup(func() { _ = s3.Close() })
	fi, _ = os.Stat(filepath.Dir(nested))
	if got := fi.Mode().Perm(); got != 0o700 {
		t.Fatalf("created data directory mode = %04o, want 0700", got)
	}
}
