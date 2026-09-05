package blobtest

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/blob"
)

// RunURLerContract is the optional sub-suite for drivers that implement blob.URLer
// (spec 003, FR-203). A driver's test calls it in addition to RunBlobContract; a driver
// that does not implement the capability simply never calls it. The suite pins the URL
// shapes the codes service relies on and, when the presigned link is an http(s) URL,
// fetches it and compares the bytes — the only proof that a signed link actually works
// against the endpoint it was signed for.
func RunURLerContract(t *testing.T, newBlob func(t *testing.T) blob.BlobStore) {
	t.Helper()
	ctx := context.Background()

	t.Run("implements URLer", func(t *testing.T) {
		if _, ok := newBlob(t).(blob.URLer); !ok {
			t.Fatalf("store does not implement blob.URLer")
		}
	})

	t.Run("public url shape", func(t *testing.T) {
		u := newBlob(t).(blob.URLer)
		for _, base := range []string{"https://cdn.example", "https://cdn.example/", "https://cdn.example/codes"} {
			got, err := u.PublicURL("codes/ab/cd/cod_x.png", base)
			if err != nil {
				t.Fatalf("PublicURL(base=%q): %v", base, err)
			}
			want := strings.TrimRight(base, "/") + "/codes/ab/cd/cod_x.png"
			if got != want {
				t.Fatalf("PublicURL(base=%q) = %q, want %q", base, got, want)
			}
		}
		if _, err := u.PublicURL("../x", "https://cdn.example"); err == nil {
			t.Fatalf("PublicURL with an unsafe key must fail")
		}
		if _, err := u.PublicURL("codes/x.png", ""); err == nil {
			t.Fatalf("PublicURL with an empty base must fail")
		}
	})

	t.Run("presigned url fetches the object", func(t *testing.T) {
		store := newBlob(t)
		u := store.(blob.URLer)
		content := []byte("presigned-content")
		const key = "presign/key.png"
		if _, err := store.Put(ctx, key, bytes.NewReader(content), int64(len(content)), "image/png"); err != nil {
			t.Fatalf("Put: %v", err)
		}
		link, err := u.PresignedURL(ctx, key, time.Minute)
		if err != nil {
			t.Fatalf("PresignedURL: %v", err)
		}
		if !strings.Contains(link, key) {
			t.Fatalf("presigned URL %q does not name the key %q", link, key)
		}
		if _, err := u.PresignedURL(ctx, key, 0); err == nil {
			t.Fatalf("PresignedURL with a zero ttl must fail")
		}
		if _, err := u.PresignedURL(ctx, "../x", time.Minute); err == nil {
			t.Fatalf("PresignedURL with an unsafe key must fail")
		}
		if !strings.HasPrefix(link, "http://") && !strings.HasPrefix(link, "https://") {
			return // an in-memory reference implementation; nothing to fetch
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET presigned URL: %v", err)
		}
		defer func() { _ = res.Body.Close() }()
		got, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET presigned URL: %d %s", res.StatusCode, got)
		}
		if !bytes.Equal(got, content) {
			t.Fatalf("presigned GET returned %q, want %q", got, content)
		}
	})
}
