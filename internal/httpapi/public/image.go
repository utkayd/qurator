package public

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/utkayd/qurator/internal/blob"
	"github.com/utkayd/qurator/internal/codes"
	"github.com/utkayd/qurator/internal/httpapi"
)

// ImageCacheControl: a code's image is immutable by contract (FR-010), so clients may
// cache it forever; the ETag lets a stale-cached client confirm freshness cheaply.
const ImageCacheControl = "public, max-age=31536000, immutable"

// fileRe accepts `<code id>.png`. Only PNG is persisted for dynamic codes in v1; any
// other extension, or a malformed id, is a 404 before the blob store is consulted.
var fileRe = regexp.MustCompile(`^(cod_[0-9a-hjkmnp-tv-z]{16})\.(png)$`)

// image serves GET /i/{file}: Stat for the ETag → 304 on If-None-Match → stream Get.
// Blob-store failures here never affect /r/{code} (Edge Cases: "Blob store unavailable").
func (h *PublicHandler) image(w http.ResponseWriter, r *http.Request) {
	if h.noImages {
		// images.serve_via_instance=false: the operator serves images from the bucket or
		// a CDN and this instance is out of the image path entirely (FR-204).
		httpapi.WriteError(w, httpapi.CodeNotFound, "No such image.", nil)
		return
	}
	m := fileRe.FindStringSubmatch(r.PathValue("file"))
	if m == nil {
		httpapi.WriteError(w, httpapi.CodeNotFound, "No such image.", nil)
		return
	}
	key := codes.BlobKeyFor(m[1])
	info, err := h.blob.Stat(r.Context(), key)
	if err != nil {
		if errors.Is(err, blob.ErrBlobNotFound) || errors.Is(err, blob.ErrInvalidKey) {
			httpapi.WriteError(w, httpapi.CodeNotFound, "No such image.", nil)
			return
		}
		httpapi.Internal(w, r, err)
		return
	}
	etag := `"` + info.ETag + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", ImageCacheControl)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	rc, info, err := h.blob.Get(r.Context(), key)
	if err != nil {
		if errors.Is(err, blob.ErrBlobNotFound) {
			httpapi.WriteError(w, httpapi.CodeNotFound, "No such image.", nil)
			return
		}
		httpapi.Internal(w, r, err)
		return
	}
	defer func() { _ = rc.Close() }()
	ct := info.ContentType
	if ct == "" {
		ct = "image/png"
	}
	w.Header().Set("Content-Type", ct)
	if info.Size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	}
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, rc); err != nil {
		slog.DebugContext(r.Context(), "image: streaming body", "err", err)
	}
}

// etagMatches implements If-None-Match: a comma-separated list, `*`, and weak
// validators (W/"...") all compare against the strong stored ETag.
func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, cand := range strings.Split(header, ",") {
		cand = strings.TrimSpace(cand)
		if cand == "*" {
			return true
		}
		cand = strings.TrimPrefix(cand, "W/")
		if cand == etag {
			return true
		}
	}
	return false
}
