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

// URLer is the optional capability of a BlobStore that can hand out addresses for its
// objects that bypass this instance entirely (spec 003, FR-203). It is detected by type
// assertion: the S3 driver implements it, the filesystem driver does not, and callers
// that find it absent simply omit storage URLs. Drivers that implement it must pass
// blobtest.RunURLerContract.
type URLer interface {
	// PublicURL joins an already-normalised base (no trailing slash) with key. It never
	// touches the network and does not prove the object is reachable — that is the
	// operator's bucket policy or CDN.
	PublicURL(key string, base string) (string, error)
	// PresignedURL returns a link that fetches key without credentials for ttl. It is
	// computed per call and never stored.
	PresignedURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}
