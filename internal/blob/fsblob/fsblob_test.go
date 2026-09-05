package fsblob

import (
	"testing"

	"github.com/utkayd/qurator/internal/blob"
	"github.com/utkayd/qurator/internal/blob/blobtest"
)

func TestFSBlobContract(t *testing.T) {
	blobtest.RunBlobContract(t, func(t *testing.T) blob.BlobStore {
		t.Helper()
		b, err := blob.Open(t.Context(), "fs", blob.Config{Path: t.TempDir()})
		if err != nil {
			t.Fatalf("open fs blob: %v", err)
		}
		return b
	})
}

// TestFSBlobHasNoURLer pins spec 003 FR-203: the filesystem driver cannot address its
// files from outside the instance, so it must NOT implement blob.URLer — storage URLs
// are absent, never fabricated.
func TestFSBlobHasNoURLer(t *testing.T) {
	s, err := blob.Open(t.Context(), "fs", blob.Config{Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(blob.URLer); ok {
		t.Fatalf("fsblob must not implement blob.URLer")
	}
}
