package fsblob_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/utkayd/qurator/internal/blob"
)

// F14: blob directories are created 0700 so a pre-existing 0755 data directory does
// not expose every stored image to other local users.
func TestFSBlobDirectoriesArePrivate(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := blob.Open(t.Context(), "fs", blob.Config{Path: filepath.Join(root, "blobs")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := b.Put(t.Context(), "codes/abc.png", strings.NewReader("png"), 3, "image/png"); err != nil {
		t.Fatalf("put: %v", err)
	}
	var checked int
	err = filepath.WalkDir(filepath.Join(root, "blobs"), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		checked++
		if got := fi.Mode().Perm(); got != 0o700 {
			t.Errorf("%s mode = %04o, want 0700", p, got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 5 { // blobs, objects, meta, and the two hashed levels under each
		t.Fatalf("walked only %d directories", checked)
	}
}
