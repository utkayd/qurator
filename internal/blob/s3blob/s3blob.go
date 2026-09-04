package s3blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/utkayd/qurator/internal/blob"
)

func init() { blob.Register("s3", Open) }

// Store is the S3-compatible driver over minio-go (research.md §3: purpose-built for
// MinIO/Garage/R2/Backblaze, a fraction of the AWS SDK's weight). ETags are whatever
// the service reports; S3 guarantees they are stable for identical single-part content
// and every method returns the same value for the same object, which is all the
// contract needs.
type Store struct {
	client *minio.Client
	bucket string
}

var _ blob.BlobStore = (*Store)(nil)

// Open builds the client. Endpoint may carry a scheme; if it does it overrides UseSSL.
func Open(_ context.Context, cfg blob.Config) (blob.BlobStore, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, errors.New("s3blob: endpoint and bucket are required")
	}
	endpoint, secure := cfg.Endpoint, cfg.UseSSL
	if strings.Contains(endpoint, "://") {
		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, fmt.Errorf("s3blob: endpoint: %w", err)
		}
		endpoint = u.Host
		secure = u.Scheme == "https"
	}
	lookup := minio.BucketLookupAuto
	if cfg.PathStyle {
		lookup = minio.BucketLookupPath
	}
	opts := &minio.Options{Secure: secure, Region: cfg.Region, BucketLookup: lookup}
	if cfg.AccessKey != "" || cfg.SecretKey != "" {
		opts.Creds = credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, "")
	} else {
		opts.Creds = credentials.NewIAM("")
	}
	c, err := minio.New(endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("s3blob: client: %w", err)
	}
	return &Store{client: c, bucket: cfg.Bucket}, nil
}

func translate(err error) error {
	if err == nil {
		return nil
	}
	resp := minio.ToErrorResponse(err)
	switch resp.Code {
	case "NoSuchKey", "NotFound":
		return fmt.Errorf("s3blob: %w", blob.ErrBlobNotFound)
	}
	return fmt.Errorf("s3blob: %w", err)
}

func trimETag(s string) string { return strings.Trim(s, `"`) }

func (s *Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (string, error) {
	if err := blob.ValidateKey(key); err != nil {
		return "", err
	}
	info, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return "", translate(err)
	}
	return trimETag(info.ETag), nil
}

func toInfo(key string, oi minio.ObjectInfo) *blob.BlobInfo {
	return &blob.BlobInfo{
		Key:         key,
		Size:        oi.Size,
		ContentType: oi.ContentType,
		ETag:        trimETag(oi.ETag),
		ModTime:     oi.LastModified.UTC(),
	}
}

// Get opens the object and stats it eagerly: minio's GetObject is lazy, so a missing
// key would otherwise surface only on the first Read.
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, *blob.BlobInfo, error) {
	if err := blob.ValidateKey(key); err != nil {
		return nil, nil, err
	}
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, nil, translate(err)
	}
	oi, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		return nil, nil, translate(err)
	}
	return obj, toInfo(key, oi), nil
}

func (s *Store) Stat(ctx context.Context, key string) (*blob.BlobInfo, error) {
	if err := blob.ValidateKey(key); err != nil {
		return nil, err
	}
	oi, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return nil, translate(err)
	}
	return toInfo(key, oi), nil
}

// Delete stats first because S3's DeleteObject succeeds silently on a missing key and
// the contract requires ErrBlobNotFound (requirement 4).
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := blob.ValidateKey(key); err != nil {
		return err
	}
	if _, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{}); err != nil {
		return translate(err)
	}
	return translate(s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}))
}

// Ping confirms the bucket exists and is reachable with the configured credentials.
func (s *Store) Ping(ctx context.Context) error {
	ok, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("s3blob: %w", err)
	}
	if !ok {
		return fmt.Errorf("s3blob: bucket %q does not exist", s.bucket)
	}
	return nil
}
