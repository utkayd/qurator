package blob

import (
	"context"
	"io"
	"time"
)

// BlobInfo describes a stored object.
type BlobInfo struct {
	Key         string
	Size        int64
	ContentType string
	ETag        string
	ModTime     time.Time
}

// BlobStore is the object persistence contract. Every driver must pass
// blobtest.RunBlobContract unmodified. ETag must be stable across Put/Get/Stat for the
// same content: the public image path serves conditional requests from it.
type BlobStore interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (etag string, err error)
	Get(ctx context.Context, key string) (io.ReadCloser, *BlobInfo, error)
	Stat(ctx context.Context, key string) (*BlobInfo, error)
	Delete(ctx context.Context, key string) error
	Ping(ctx context.Context) error
}
