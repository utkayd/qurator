package s3blob

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/utkayd/qurator/internal/blob"
	"github.com/utkayd/qurator/internal/blob/blobtest"
)

// TestS3BlobContract runs the contract suite against a live S3-compatible endpoint
// (MinIO in CI). Each newBlob call gets a fresh bucket that is emptied and removed on
// cleanup, so subtests never observe one another's objects.
func TestS3BlobContract(t *testing.T) {
	endpoint := os.Getenv("QURATOR_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("QURATOR_TEST_S3_ENDPOINT not set; skipping S3 contract tests")
	}
	access := envOr("QURATOR_TEST_S3_ACCESS_KEY", "minioadmin")
	secret := envOr("QURATOR_TEST_S3_SECRET_KEY", "minioadmin")
	useSSL, _ := strconv.ParseBool(envOr("QURATOR_TEST_S3_USE_SSL", "false"))

	admin, err := minio.New(endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(access, secret, ""),
		Secure:       useSSL,
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		t.Fatalf("minio client: %v", err)
	}

	blobtest.RunBlobContract(t, func(t *testing.T) blob.BlobStore {
		t.Helper()
		var b [6]byte
		if _, err := rand.Read(b[:]); err != nil {
			t.Fatal(err)
		}
		bucket := "qurator-test-" + hex.EncodeToString(b[:])
		if err := admin.MakeBucket(t.Context(), bucket, minio.MakeBucketOptions{}); err != nil {
			t.Fatalf("make bucket: %v", err)
		}
		t.Cleanup(func() {
			ctx := context.Background()
			for obj := range admin.ListObjects(ctx, bucket, minio.ListObjectsOptions{Recursive: true}) {
				if obj.Err == nil {
					_ = admin.RemoveObject(ctx, bucket, obj.Key, minio.RemoveObjectOptions{})
				}
			}
			_ = admin.RemoveBucket(ctx, bucket)
		})
		s, err := blob.Open(t.Context(), "s3", blob.Config{
			Endpoint:  endpoint,
			Bucket:    bucket,
			AccessKey: access,
			SecretKey: secret,
			UseSSL:    useSSL,
			PathStyle: true,
		})
		if err != nil {
			t.Fatalf("open s3 blob: %v", err)
		}
		return s
	})
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
