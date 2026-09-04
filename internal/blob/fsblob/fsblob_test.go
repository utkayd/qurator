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
