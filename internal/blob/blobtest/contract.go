// Package blobtest provides a shared conformance suite for blob.BlobStore
// implementations, plus an in-memory reference implementation to run it
// against.
package blobtest

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/utkayd/qurator/internal/blob"
)

// RunBlobContract exercises the blob.BlobStore contract requirements from
// contracts/store.md against a fresh store produced by newBlob for every
// subtest. Every BlobStore driver must pass this suite unmodified.
func RunBlobContract(t *testing.T, newBlob func(t *testing.T) blob.BlobStore) {
	t.Helper()
	ctx := context.Background()

	t.Run("round-trip fidelity", func(t *testing.T) {
		store := newBlob(t)

		cases := map[string][]byte{
			"empty":    {},
			"with-nul": []byte("hello\x00world\x00\x00"),
			"large-5mb": func() []byte {
				b := make([]byte, 5*1024*1024)
				if _, err := rand.Read(b); err != nil {
					t.Fatalf("rand.Read: %v", err)
				}
				return b
			}(),
		}

		for name, content := range cases {
			name, content := name, content
			t.Run(name, func(t *testing.T) {
				key := "roundtrip/" + name
				if _, err := store.Put(ctx, key, bytes.NewReader(content), int64(len(content)), "application/octet-stream"); err != nil {
					t.Fatalf("Put: %v", err)
				}

				rc, info, err := store.Get(ctx, key)
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				defer rc.Close()

				got, err := io.ReadAll(rc)
				if err != nil {
					t.Fatalf("ReadAll: %v", err)
				}
				if !bytes.Equal(got, content) {
					t.Fatalf("content mismatch: got %d bytes, want %d bytes", len(got), len(content))
				}
				if info.Size != int64(len(content)) {
					t.Fatalf("Size = %d, want %d", info.Size, len(content))
				}
			})
		}
	})

	t.Run("etag stability", func(t *testing.T) {
		store := newBlob(t)

		contentA := []byte("content-a")
		contentB := []byte("content-b")

		putETag, err := store.Put(ctx, "etag/key", bytes.NewReader(contentA), int64(len(contentA)), "text/plain")
		if err != nil {
			t.Fatalf("Put: %v", err)
		}

		rc, getInfo, err := store.Get(ctx, "etag/key")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		rc.Close()

		statInfo, err := store.Stat(ctx, "etag/key")
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}

		if putETag == "" {
			t.Fatal("Put returned empty ETag")
		}
		if getInfo.ETag != putETag {
			t.Fatalf("Get ETag = %q, want %q (from Put)", getInfo.ETag, putETag)
		}
		if statInfo.ETag != putETag {
			t.Fatalf("Stat ETag = %q, want %q (from Put)", statInfo.ETag, putETag)
		}

		putETagB, err := store.Put(ctx, "etag/key-b", bytes.NewReader(contentB), int64(len(contentB)), "text/plain")
		if err != nil {
			t.Fatalf("Put: %v", err)
		}
		if putETagB == putETag {
			t.Fatalf("different content produced same ETag %q", putETag)
		}
	})

	t.Run("not found", func(t *testing.T) {
		store := newBlob(t)

		if _, _, err := store.Get(ctx, "missing/key"); !errors.Is(err, blob.ErrBlobNotFound) {
			t.Fatalf("Get missing key: err = %v, want ErrBlobNotFound", err)
		}
		if _, err := store.Stat(ctx, "missing/key"); !errors.Is(err, blob.ErrBlobNotFound) {
			t.Fatalf("Stat missing key: err = %v, want ErrBlobNotFound", err)
		}
		if err := store.Delete(ctx, "missing/key"); !errors.Is(err, blob.ErrBlobNotFound) {
			t.Fatalf("Delete missing key: err = %v, want ErrBlobNotFound", err)
		}
	})

	t.Run("delete idempotent", func(t *testing.T) {
		store := newBlob(t)

		content := []byte("to-be-deleted")
		if _, err := store.Put(ctx, "delete/key", bytes.NewReader(content), int64(len(content)), "text/plain"); err != nil {
			t.Fatalf("Put: %v", err)
		}

		if err := store.Delete(ctx, "delete/key"); err != nil {
			t.Fatalf("first Delete: %v", err)
		}
		if err := store.Delete(ctx, "delete/key"); !errors.Is(err, blob.ErrBlobNotFound) {
			t.Fatalf("second Delete: err = %v, want ErrBlobNotFound", err)
		}
	})

	t.Run("overwrite", func(t *testing.T) {
		store := newBlob(t)

		original := []byte("original-content")
		replacement := []byte("replacement-content-longer")

		etag1, err := store.Put(ctx, "overwrite/key", bytes.NewReader(original), int64(len(original)), "text/plain")
		if err != nil {
			t.Fatalf("first Put: %v", err)
		}

		etag2, err := store.Put(ctx, "overwrite/key", bytes.NewReader(replacement), int64(len(replacement)), "text/plain")
		if err != nil {
			t.Fatalf("second Put: %v", err)
		}

		if etag1 == etag2 {
			t.Fatalf("overwrite with different content produced same ETag %q", etag1)
		}

		rc, info, err := store.Get(ctx, "overwrite/key")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		defer rc.Close()

		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if !bytes.Equal(got, replacement) {
			t.Fatalf("content after overwrite = %q, want %q", got, replacement)
		}
		if info.ETag != etag2 {
			t.Fatalf("info.ETag after overwrite = %q, want %q", info.ETag, etag2)
		}
	})

	t.Run("key safety", func(t *testing.T) {
		store := newBlob(t)

		badKeys := []string{
			"../x",
			"/abs",
			"a/../b",
			"a\x00b",
		}

		content := []byte("payload")
		for _, key := range badKeys {
			key := key
			t.Run(key, func(t *testing.T) {
				if _, err := store.Put(ctx, key, bytes.NewReader(content), int64(len(content)), "text/plain"); !errors.Is(err, blob.ErrInvalidKey) {
					t.Fatalf("Put(%q): err = %v, want ErrInvalidKey", key, err)
				}
				if _, _, err := store.Get(ctx, key); !errors.Is(err, blob.ErrInvalidKey) {
					t.Fatalf("Get(%q): err = %v, want ErrInvalidKey", key, err)
				}
				if _, err := store.Stat(ctx, key); !errors.Is(err, blob.ErrInvalidKey) {
					t.Fatalf("Stat(%q): err = %v, want ErrInvalidKey", key, err)
				}
				if err := store.Delete(ctx, key); !errors.Is(err, blob.ErrInvalidKey) {
					t.Fatalf("Delete(%q): err = %v, want ErrInvalidKey", key, err)
				}
			})
		}
	})

	t.Run("concurrent put to one key", func(t *testing.T) {
		store := newBlob(t)

		const goroutines = 16
		const payloadSize = 256 * 1024

		payloads := make([][]byte, goroutines)
		for i := range payloads {
			b := make([]byte, payloadSize)
			if _, err := rand.Read(b); err != nil {
				t.Fatalf("rand.Read: %v", err)
			}
			payloads[i] = b
		}

		var wg sync.WaitGroup
		errs := make([]error, goroutines)
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, err := store.Put(ctx, "concurrent/key", bytes.NewReader(payloads[i]), int64(len(payloads[i])), "application/octet-stream")
				errs[i] = err
			}(i)
		}
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Fatalf("Put goroutine %d: %v", i, err)
			}
		}

		rc, info, err := store.Get(ctx, "concurrent/key")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		defer rc.Close()

		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}

		if len(got) != payloadSize {
			t.Fatalf("final content length = %d, want %d (no partial/corrupted write)", len(got), payloadSize)
		}
		if info.Size != payloadSize {
			t.Fatalf("info.Size = %d, want %d", info.Size, payloadSize)
		}

		matched := -1
		for i, p := range payloads {
			if bytes.Equal(got, p) {
				matched = i
				break
			}
		}
		if matched == -1 {
			t.Fatal("final content does not byte-equal any single payload (mixed or corrupted write)")
		}
	})

	t.Run("content type round-trip", func(t *testing.T) {
		store := newBlob(t)

		content := []byte("typed-content")
		const wantType = "image/svg+xml"

		if _, err := store.Put(ctx, "type/key", bytes.NewReader(content), int64(len(content)), wantType); err != nil {
			t.Fatalf("Put: %v", err)
		}

		info, err := store.Stat(ctx, "type/key")
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if info.ContentType != wantType {
			t.Fatalf("Stat ContentType = %q, want %q", info.ContentType, wantType)
		}

		rc, getInfo, err := store.Get(ctx, "type/key")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		rc.Close()
		if getInfo.ContentType != wantType {
			t.Fatalf("Get ContentType = %q, want %q", getInfo.ContentType, wantType)
		}
	})

	t.Run("ping", func(t *testing.T) {
		store := newBlob(t)
		if err := store.Ping(ctx); err != nil {
			t.Fatalf("Ping: %v", err)
		}
	})
}
