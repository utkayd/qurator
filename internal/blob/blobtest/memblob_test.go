package blobtest

import (
	"testing"

	"github.com/utkayd/qurator/internal/blob"
)

func TestMemBlobContract(t *testing.T) {
	RunBlobContract(t, func(t *testing.T) blob.BlobStore {
		return NewMemBlob()
	})
}

func TestMemBlobURLer(t *testing.T) {
	RunURLerContract(t, func(t *testing.T) blob.BlobStore {
		return NewMemBlob()
	})
}
