// Package contract holds HTTP-level contract tests for the OpenAPI surface.
package contract

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/utkayd/qurator/internal/config"
	"github.com/utkayd/qurator/internal/httpapi"
	v1 "github.com/utkayd/qurator/internal/httpapi/v1"
	"github.com/utkayd/qurator/internal/qr"
	"github.com/utkayd/qurator/tools/qrdecode/decode"
)

// fakeLimiter is an injected rate limiter: it allows `burst` requests then answers
// 429 rate_limited in the contract's error shape.
type fakeLimiter struct {
	burst int32
	seen  atomic.Int32
}

func (f *fakeLimiter) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.seen.Add(1) > f.burst {
			w.Header().Set("Retry-After", "1")
			httpapi.WriteError(w, httpapi.CodeRateLimited, "Too many requests.", map[string]any{"retry_after_s": 1})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func newHandler(t *testing.T, public bool, authed bool, limiter func(http.Handler) http.Handler) http.Handler {
	t.Helper()
	renderer := qr.NewRenderer(qr.Bounds{MaxPx: 1024, MaxDuration: 0, MaxPayload: 2953})
	isAuthed := func(*http.Request) bool { return authed }
	return v1.NewQRHandler(renderer, config.EphemeralConfig{Public: public, RateLimitPerMinute: 60}, isAuthed, limiter)
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("error response Content-Type = %q, want application/json; body: %s", ct, rec.Body.String())
	}
	var body httpapi.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not the contract shape: %v: %s", err, rec.Body.String())
	}
	return string(body.Error.Code)
}

func get(h http.Handler, q url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/v1/qr?"+q.Encode(), nil)
	req.RemoteAddr = "203.0.113.7:4242"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func post(h http.Handler, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/qr", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.7:4242"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestQR_PNGContentType(t *testing.T) {
	h := newHandler(t, false, true, nil)
	rec := get(h, url.Values{"content": {"hello"}, "format": {"png"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if et := rec.Header().Get("ETag"); !strings.HasPrefix(et, `"`) || !strings.HasSuffix(et, `"`) {
		t.Errorf("ETag = %q, want a quoted entity tag", et)
	}
	res, err := decode.PNG(rec.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Bytes) != "hello" {
		t.Errorf("decoded %q, want hello", res.Bytes)
	}
}

func TestQR_SVGContentType(t *testing.T) {
	h := newHandler(t, false, true, nil)
	rec := post(h, map[string]any{"content": "hello", "format": "svg"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("Content-Type = %q, want image/svg+xml", ct)
	}
	res, err := decode.SVG(rec.Body.Bytes(), 512)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Bytes) != "hello" {
		t.Errorf("decoded %q, want hello", res.Bytes)
	}
}

func TestQR_DefaultFormatIsPNG(t *testing.T) {
	h := newHandler(t, false, true, nil)
	rec := get(h, url.Values{"content": {"hello"}})
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("status = %d, Content-Type = %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestQR_ContentTooLarge(t *testing.T) {
	h := newHandler(t, false, true, nil)
	rec := post(h, map[string]any{"content": strings.Repeat("x", 2954), "styling": map[string]any{"ec_level": "L"}})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body = %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "content_too_large" {
		t.Errorf("code = %q, want content_too_large", code)
	}
	var body httpapi.ErrorBody
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Details["limit_bytes"] == nil || body.Error.Details["actual_bytes"] == nil {
		t.Errorf("details must name the limit: %v", body.Error.Details)
	}
}

func TestQR_UnauthorizedByDefault(t *testing.T) {
	h := newHandler(t, false, false, nil)
	rec := get(h, url.Values{"content": {"hello"}})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body = %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "unauthorized" {
		t.Errorf("code = %q, want unauthorized", code)
	}
}

func TestQR_PublicAllowsAnonymous(t *testing.T) {
	h := newHandler(t, true, false, (&fakeLimiter{burst: 100}).wrap)
	rec := get(h, url.Values{"content": {"hello"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
}

func TestQR_RateLimitedWhenPublic(t *testing.T) {
	h := newHandler(t, true, false, (&fakeLimiter{burst: 3}).wrap)
	for i := 0; i < 3; i++ {
		if rec := get(h, url.Values{"content": {"hello"}}); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d", i, rec.Code)
		}
	}
	rec := get(h, url.Values{"content": {"hello"}})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if code := errorCode(t, rec); code != "rate_limited" {
		t.Errorf("code = %q, want rate_limited", code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header missing")
	}
}

func TestQR_LimiterNotAppliedWhenPrivate(t *testing.T) {
	lim := &fakeLimiter{burst: 0}
	h := newHandler(t, false, true, lim.wrap)
	if rec := get(h, url.Values{"content": {"hello"}}); rec.Code != http.StatusOK {
		t.Fatalf("private instance must not rate-limit authenticated callers: status = %d", rec.Code)
	}
}

func TestQR_ByteIdenticalForIdenticalParams(t *testing.T) {
	h := newHandler(t, false, true, nil)
	q := url.Values{"content": {"same"}, "format": {"png"}, "size_px": {"300"}, "ec_level": {"Q"}}
	a := get(h, q)
	b := get(h, q)
	if a.Code != http.StatusOK || b.Code != http.StatusOK {
		t.Fatalf("status = %d / %d", a.Code, b.Code)
	}
	if !bytes.Equal(a.Body.Bytes(), b.Body.Bytes()) {
		t.Error("identical params produced different bodies")
	}
	if a.Header().Get("ETag") != b.Header().Get("ETag") {
		t.Error("identical bodies produced different ETags")
	}
	// GET and POST with the same parameters must also agree.
	c := post(h, map[string]any{"content": "same", "format": "png", "styling": map[string]any{"size_px": 300, "ec_level": "Q"}})
	if !bytes.Equal(a.Body.Bytes(), c.Body.Bytes()) {
		t.Error("GET and POST with the same parameters produced different bodies")
	}
}

func TestQR_IfNoneMatch(t *testing.T) {
	h := newHandler(t, false, true, nil)
	first := get(h, url.Values{"content": {"cached"}})
	etag := first.Header().Get("ETag")
	req := httptest.NewRequest(http.MethodGet, "/v1/qr?content=cached", nil)
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Error("304 must carry no body")
	}
}

func TestQR_InvalidParams(t *testing.T) {
	h := newHandler(t, false, true, nil)
	cases := []url.Values{
		{"content": {""}},
		{"content": {"x"}, "format": {"gif"}},
		{"content": {"x"}, "fg_color": {"red"}},
		{"content": {"x"}, "margin_modules": {"2"}},
		{"content": {"x"}, "size_px": {"10"}},
		{"content": {"x"}, "ec_level": {"X"}},
		{"content": {"x"}, "module_shape": {"hexagon"}},
	}
	for _, q := range cases {
		rec := get(h, q)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%v: status = %d, want 400", q, rec.Code)
			continue
		}
		if code := errorCode(t, rec); code != "invalid_request" {
			t.Errorf("%v: code = %q, want invalid_request", q, code)
		}
	}
}

func TestQR_DimensionsExceeded(t *testing.T) {
	h := newHandler(t, false, true, nil) // MaxPx 1024
	rec := get(h, url.Values{"content": {"x"}, "size_px": {"2048"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "dimensions_exceeded" {
		t.Errorf("code = %q, want dimensions_exceeded", code)
	}
}

func TestQR_UnknownJSONFieldRejected(t *testing.T) {
	h := newHandler(t, false, true, nil)
	rec := post(h, map[string]any{"content": "x", "bogus": true})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
