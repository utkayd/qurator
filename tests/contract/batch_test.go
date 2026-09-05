package contract

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/blob"
	"github.com/utkayd/qurator/internal/blob/blobtest"
	"github.com/utkayd/qurator/internal/codes"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/store"
	"github.com/utkayd/qurator/internal/store/storetest"
)

func (f *fixture) batch(t *testing.T, user string, items []map[string]any) (*http.Response, []map[string]any, map[string]any) {
	t.Helper()
	res, body := f.do(t, user, http.MethodPost, "/v1/codes/batch", map[string]any{"items": items}, nil)
	raw, _ := body["results"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.(map[string]any))
	}
	return res, out, body
}

func statusesOf(results []map[string]any) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r["status"].(string))
	}
	return out
}

func itemErr(t *testing.T, r map[string]any) (string, map[string]any) {
	t.Helper()
	e, ok := r["error"].(map[string]any)
	if !ok {
		t.Fatalf("result %v has no error", r)
	}
	details, _ := e["details"].(map[string]any)
	return e["code"].(string), details
}

func (f *fixture) countCodes(t *testing.T, user string) int {
	t.Helper()
	res, body := f.do(t, user, http.MethodGet, "/v1/codes?limit=200", nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %v", res.StatusCode, body)
	}
	return len(body["items"].([]any))
}

// TestBatch_PerItemResults pins US3 scenario 1 / FR-205: one result per item at its
// input index, each a created code or that item's own error envelope, HTTP 200 overall.
func TestBatch_PerItemResults(t *testing.T) {
	f := newBatchFixture(t, codes.Config{}, nil)
	res, results, _ := f.batch(t, "alice", []map[string]any{
		{"destination": "https://example.com/0"},
		{"destination": "javascript:alert(1)"},
		{"destination": "https://example.com/2", "alias": "batch-alias", "mode": "direct", "styling": map[string]any{"module_shape": "dot"}},
		{"destination": ""},
		{"destination": "https://example.com/4", "alias": "batch-alias"},
		{"destination": "https://example.com/5", "styling": map[string]any{"size_px": 1}},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("batch: %d", res.StatusCode)
	}
	want := []string{"created", "error", "created", "error", "error", "error"}
	if got := statusesOf(results); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}
	for i, r := range results {
		if int(r["index"].(float64)) != i {
			t.Fatalf("result %d carries index %v", i, r["index"])
		}
		_, hasCode := r["code"]
		_, hasErr := r["error"]
		if hasCode == hasErr {
			t.Fatalf("result %d must carry exactly one of code/error: %v", i, r)
		}
	}
	if code, details := itemErr(t, results[1]); code != "unsupported_scheme" || details["scheme"] != "javascript" {
		t.Fatalf("item 1: %v", results[1])
	}
	if code, details := itemErr(t, results[3]); code != "invalid_request" || details["field"] != "destination" {
		t.Fatalf("item 3: %v", results[3])
	}
	if code, details := itemErr(t, results[4]); code != "alias_taken" || details["alias"] != "batch-alias" {
		t.Fatalf("item 4 (alias repeated within the batch): %v", results[4])
	}
	if code, details := itemErr(t, results[5]); code != "invalid_request" || details["field"] != "styling.size_px" {
		t.Fatalf("item 5: %v", results[5])
	}
	c2 := results[2]["code"].(map[string]any)
	if c2["mode"] != "direct" || c2["short_code"] != "batch-alias" || c2["styling"].(map[string]any)["module_shape"] != "dot" {
		t.Fatalf("item 2 lost its own options: %v", c2)
	}
	if _, present := c2["scan_url"]; present {
		t.Fatalf("direct batch item must omit scan_url")
	}
	c0 := results[0]["code"].(map[string]any)
	if c0["mode"] != "dynamic" || c0["scan_url"] != testBaseURL+"/r/"+c0["short_code"].(string) {
		t.Fatalf("item 0: %v", c0)
	}
	// Created items are real: readable, listed, image stored, scannable.
	for _, c := range []map[string]any{c0, c2} {
		id := c["id"].(string)
		if res, _ := f.do(t, "alice", http.MethodGet, "/v1/codes/"+id, nil, nil); res.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d", id, res.StatusCode)
		}
		if _, err := f.blob.Stat(context.Background(), codes.BlobKeyFor(id)); err != nil {
			t.Fatalf("image for %s: %v", id, err)
		}
	}
	assertRedirect(t, scan(f, "/r/batch-alias", nil), "https://example.com/2")
	if n := f.countCodes(t, "alice"); n != 2 {
		t.Fatalf("%d codes listed, want 2", n)
	}
	// Failed items left no image behind: only two objects were ever kept.
	if n := countBlobs(t, f.blob); n != 2 {
		t.Fatalf("%d blobs stored, want 2", n)
	}
	// Anonymous callers are refused like any protected route.
	res, _, body := f.batch(t, "", []map[string]any{{"destination": "https://example.com/x"}})
	if code, _ := errCodeCodes(t, body); res.StatusCode != http.StatusUnauthorized || code != "unauthorized" {
		t.Fatalf("anonymous batch: %d %v", res.StatusCode, body)
	}
}

// countBlobs counts stored objects by probing the keys tests could have produced; the
// memblob has no listing, so it walks the fixture's codes and logo keys.
func countBlobs(t *testing.T, b blob.BlobStore) int {
	t.Helper()
	tb, ok := b.(*trackingBlob)
	if !ok {
		t.Fatalf("countBlobs needs the tracking blob")
	}
	return tb.live()
}

// trackingBlob wraps the memblob and records which keys are live, so tests can prove a
// failed batch removed every image it wrote. It forwards blob.URLer too.
type trackingBlob struct {
	blob.BlobStore
	mu   sync.Mutex
	keys map[string]bool
	// failPut, when set, makes Put fail for keys containing the substring.
	failPut string
}

func newTrackingBlob() *trackingBlob {
	return &trackingBlob{BlobStore: blobtest.NewMemBlob(), keys: map[string]bool{}}
}

func (b *trackingBlob) Put(ctx context.Context, key string, r io.Reader, size int64, ct string) (string, error) {
	if b.failPut != "" && strings.Contains(key, b.failPut) {
		return "", errors.New("tracking blob: injected put failure")
	}
	etag, err := b.BlobStore.Put(ctx, key, r, size, ct)
	if err == nil {
		b.mu.Lock()
		b.keys[key] = true
		b.mu.Unlock()
	}
	return etag, err
}

func (b *trackingBlob) Delete(ctx context.Context, key string) error {
	err := b.BlobStore.Delete(ctx, key)
	if err == nil {
		b.mu.Lock()
		delete(b.keys, key)
		b.mu.Unlock()
	}
	return err
}

func (b *trackingBlob) PublicURL(key, base string) (string, error) {
	return b.BlobStore.(blob.URLer).PublicURL(key, base)
}

func (b *trackingBlob) PresignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return b.BlobStore.(blob.URLer).PresignedURL(ctx, key, ttl)
}

func (b *trackingBlob) live() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.keys)
}

// newBatchFixture is a fixture over the tracking blob, so tests can count objects.
func newBatchFixture(t *testing.T, cfg codes.Config, st store.Store) *fixture {
	t.Helper()
	return newFixtureOpts(t, fixtureOpts{store: st, blob: newTrackingBlob(), cfg: cfg})
}

// TestBatch_ClientRefIdempotency pins US3 scenario 2 / FR-206: re-posting a batch with
// the same client_refs returns the same codes as existing and creates nothing; a ref
// reused with a different destination or mode is a per-item 409 naming the holder.
func TestBatch_ClientRefIdempotency(t *testing.T) {
	f := newBatchFixture(t, codes.Config{}, nil)
	items := []map[string]any{
		{"destination": "https://example.com/1", "client_ref": "order-1"},
		{"destination": "https://example.com/2", "client_ref": "order-2", "mode": "direct"},
		{"destination": "https://example.com/3"},
	}
	res, first, _ := f.batch(t, "alice", items)
	if res.StatusCode != http.StatusOK || fmt.Sprint(statusesOf(first)) != "[created created created]" {
		t.Fatalf("first batch: %d %v", res.StatusCode, first)
	}
	if first[0]["code"].(map[string]any)["client_ref"] != "order-1" {
		t.Fatalf("created code must echo its client_ref: %v", first[0])
	}
	if _, present := first[2]["code"].(map[string]any)["client_ref"]; present {
		t.Fatalf("a code without client_ref must omit the key: %v", first[2])
	}

	// Repost: refs come back as existing with the same ids; the ref-less item is new.
	res, again, _ := f.batch(t, "alice", items)
	if res.StatusCode != http.StatusOK || fmt.Sprint(statusesOf(again)) != "[existing existing created]" {
		t.Fatalf("repost: %d %v", res.StatusCode, statusesOf(again))
	}
	for i := 0; i < 2; i++ {
		if again[i]["code"].(map[string]any)["id"] != first[i]["code"].(map[string]any)["id"] {
			t.Fatalf("item %d: existing id differs", i)
		}
	}
	if n := f.countCodes(t, "alice"); n != 4 {
		t.Fatalf("%d codes after repost, want 4 (3 + the ref-less repeat)", n)
	}
	if n := countBlobs(t, f.blob); n != 4 {
		t.Fatalf("%d blobs after repost, want 4", n)
	}

	// Same ref, different destination or mode: 409 for that item, pointing at the holder.
	res, conflict, _ := f.batch(t, "alice", []map[string]any{
		{"destination": "https://example.com/changed", "client_ref": "order-1"},
		{"destination": "https://example.com/2", "client_ref": "order-2"}, // mode differs (dynamic vs direct)
		{"destination": "https://example.com/3", "client_ref": "order-3"}, // new ref: created
	})
	if res.StatusCode != http.StatusOK || fmt.Sprint(statusesOf(conflict)) != "[error error created]" {
		t.Fatalf("conflict batch: %d %v", res.StatusCode, statusesOf(conflict))
	}
	for i := 0; i < 2; i++ {
		code, details := itemErr(t, conflict[i])
		if code != "client_ref_conflict" || details["existing_id"] != first[i]["code"].(map[string]any)["id"] || details["client_ref"] != items[i]["client_ref"] {
			t.Fatalf("item %d: %v", i, conflict[i])
		}
	}

	// Refs are per user: bob may use alice's.
	res, bobs, _ := f.batch(t, "bob", []map[string]any{{"destination": "https://example.com/bob", "client_ref": "order-1"}})
	if res.StatusCode != http.StatusOK || bobs[0]["status"] != "created" {
		t.Fatalf("bob reusing alice's ref: %v", bobs)
	}

	// Within one batch a ref may appear once; the second occurrence is that item's error.
	// An over-long ref is rejected before anything is rendered.
	res, dup, _ := f.batch(t, "alice", []map[string]any{
		{"destination": "https://example.com/d", "client_ref": "dup"},
		{"destination": "https://example.com/d", "client_ref": "dup"},
		{"destination": "https://example.com/d", "client_ref": strings.Repeat("x", 129)},
	})
	if res.StatusCode != http.StatusOK || fmt.Sprint(statusesOf(dup)) != "[created error error]" {
		t.Fatalf("duplicate/over-long refs: %d %v", res.StatusCode, statusesOf(dup))
	}
	if code, details := itemErr(t, dup[1]); code != "invalid_request" || details["field"] != "client_ref" {
		t.Fatalf("duplicate ref: %v", dup[1])
	}
	if code, details := itemErr(t, dup[2]); code != "invalid_request" || details["field"] != "client_ref" {
		t.Fatalf("over-long ref: %v", dup[2])
	}

	// The single-create endpoint honours the same key: 201 then 200 with the same code.
	res, single := f.do(t, "alice", http.MethodPost, "/v1/codes", map[string]any{"destination": "https://example.com/s", "client_ref": "single"}, nil)
	if res.StatusCode != http.StatusCreated || single["client_ref"] != "single" {
		t.Fatalf("single create with ref: %d %v", res.StatusCode, single)
	}
	res, repeat := f.do(t, "alice", http.MethodPost, "/v1/codes", map[string]any{"destination": "https://example.com/s", "client_ref": "single"}, nil)
	if res.StatusCode != http.StatusOK || repeat["id"] != single["id"] {
		t.Fatalf("single create repeated: %d %v", res.StatusCode, repeat)
	}
	res, body := f.do(t, "alice", http.MethodPost, "/v1/codes", map[string]any{"destination": "https://example.com/other", "client_ref": "single"}, nil)
	if code, details := errCodeCodes(t, body); res.StatusCode != http.StatusConflict || code != "client_ref_conflict" || details["existing_id"] != single["id"] {
		t.Fatalf("single create conflicting ref: %d %v", res.StatusCode, body)
	}
	res, body = f.do(t, "alice", http.MethodPost, "/v1/codes", map[string]any{"destination": "https://example.com/other", "client_ref": strings.Repeat("x", 129)}, nil)
	if code, details := errCodeCodes(t, body); res.StatusCode != http.StatusBadRequest || code != "invalid_request" || details["field"] != "client_ref" {
		t.Fatalf("single create over-long ref: %d %v", res.StatusCode, body)
	}
}

// TestBatch_Limits pins US3 scenario 3 / FR-205: an empty batch is 400, one over
// codes.batch_max is 413 with the limit, and neither creates anything.
func TestBatch_Limits(t *testing.T) {
	f := newBatchFixture(t, codes.Config{BatchMax: 3}, nil)

	res, _, body := f.batch(t, "alice", []map[string]any{})
	if code, details := errCodeCodes(t, body); res.StatusCode != http.StatusBadRequest || code != "invalid_request" || details["field"] != "items" {
		t.Fatalf("empty batch: %d %v", res.StatusCode, body)
	}
	res, body = f.do(t, "alice", http.MethodPost, "/v1/codes/batch", map[string]any{}, nil)
	if code, _ := errCodeCodes(t, body); res.StatusCode != http.StatusBadRequest || code != "invalid_request" {
		t.Fatalf("missing items: %d %v", res.StatusCode, body)
	}

	var items []map[string]any
	for i := 0; i < 4; i++ {
		items = append(items, map[string]any{"destination": fmt.Sprintf("https://example.com/%d", i)})
	}
	res, _, body = f.batch(t, "alice", items)
	code, details := errCodeCodes(t, body)
	if res.StatusCode != http.StatusRequestEntityTooLarge || code != "batch_too_large" {
		t.Fatalf("4 items with batch_max=3: %d %v", res.StatusCode, body)
	}
	if details["limit"] != float64(3) || details["actual"] != float64(4) {
		t.Fatalf("batch_too_large details: %v", details)
	}
	if n := f.countCodes(t, "alice"); n != 0 {
		t.Fatalf("%d codes after refused batches, want 0", n)
	}
	// Exactly the limit is fine.
	res, results, _ := f.batch(t, "alice", items[:3])
	if res.StatusCode != http.StatusOK || fmt.Sprint(statusesOf(results)) != "[created created created]" {
		t.Fatalf("3 items with batch_max=3: %d %v", res.StatusCode, statusesOf(results))
	}
}

// failingStore fails CreateCodes with the configured error, standing in for a store
// that goes away mid-batch (US3 scenario 4).
type failingStore struct {
	store.Store
	err error
}

func (s *failingStore) CreateCodes(context.Context, []*domain.Code) error { return s.err }
func (s *failingStore) CreateCode(ctx context.Context, c *domain.Code) error {
	return s.CreateCodes(ctx, []*domain.Code{c})
}

// TestBatch_AtomicOnStoreFailure pins US3 scenario 4 / FR-207: when the single
// transaction fails, every rendered item is an error, no row exists and no image is
// left behind; a lost client_ref race surfaces as the conflict code.
func TestBatch_AtomicOnStoreFailure(t *testing.T) {
	inner := storetest.NewMemStore()
	fs := &failingStore{Store: inner, err: errors.New("store: connection reset")}
	f := newBatchFixture(t, codes.Config{}, fs)
	res, results, _ := f.batch(t, "alice", []map[string]any{
		{"destination": "https://example.com/1"},
		{"destination": "   "},
		{"destination": "https://example.com/3", "client_ref": "r3"},
	})
	if res.StatusCode != http.StatusOK || fmt.Sprint(statusesOf(results)) != "[error error error]" {
		t.Fatalf("store failure: %d %v", res.StatusCode, statusesOf(results))
	}
	if code, _ := itemErr(t, results[0]); code != "internal" {
		t.Fatalf("item 0 after store failure: %v", results[0])
	}
	if code, _ := itemErr(t, results[1]); code != "invalid_request" {
		t.Fatalf("item 1 keeps its own validation error: %v", results[1])
	}
	if n := f.countCodes(t, "alice"); n != 0 {
		t.Fatalf("%d codes after a failed transaction, want 0", n)
	}
	if n := countBlobs(t, f.blob); n != 0 {
		t.Fatalf("%d blobs left behind after a failed transaction, want 0", n)
	}

	// A client_ref race lost at insert time (pre-check passed, unique index fired).
	fs.err = fmt.Errorf("driver: %w", store.ErrClientRefTaken)
	_, results, _ = f.batch(t, "alice", []map[string]any{{"destination": "https://example.com/1", "client_ref": "raced"}})
	if code, _ := itemErr(t, results[0]); results[0]["status"] != "error" || code != "client_ref_conflict" {
		t.Fatalf("lost client_ref race: %v", results[0])
	}
	if n := countBlobs(t, f.blob); n != 0 {
		t.Fatalf("%d blobs left behind after a lost race, want 0", n)
	}

	// A blob failure for one item fails that item only; the rest land in one transaction.
	f2 := newBatchFixture(t, codes.Config{}, nil)
	tb := f2.blob.(*trackingBlob)
	tb.failPut = "logos/"
	_, b64 := logoPNG(t)
	_, results, _ = f2.batch(t, "alice", []map[string]any{
		{"destination": "https://example.com/1"},
		{"destination": "https://example.com/2", "styling": map[string]any{"logo": map[string]any{"image_base64": b64}}},
		{"destination": "https://example.com/3"},
	})
	if fmt.Sprint(statusesOf(results)) != "[created error created]" {
		t.Fatalf("one blob failure: %v", statusesOf(results))
	}
	if n := f2.countCodes(t, "alice"); n != 2 {
		t.Fatalf("%d codes, want 2", n)
	}
	if n := tb.live(); n != 2 {
		t.Fatalf("%d blobs, want 2 (the failed item's image was removed)", n)
	}
}

// concurrencyRenderer counts how many renders run at once.
type concurrencyRenderer struct {
	inFlight atomic.Int32
	peak     atomic.Int32
	calls    atomic.Int32
}

func (r *concurrencyRenderer) Render(_ context.Context, content string, s domain.Styling, _ []byte, _ bool) ([]byte, domain.ECLevel, error) {
	n := r.inFlight.Add(1)
	defer r.inFlight.Add(-1)
	r.calls.Add(1)
	for {
		p := r.peak.Load()
		if n <= p || r.peak.CompareAndSwap(p, n) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	return []byte("\x89PNG\r\n\x1a\n" + content), s.ECLevel, nil
}

// TestBatch_RendersInParallelBounded pins US3 scenario 5 / FR-207: items render
// concurrently, never more than codes.batch_workers at a time.
func TestBatch_RendersInParallelBounded(t *testing.T) {
	r := &concurrencyRenderer{}
	f := newFixtureOpts(t, fixtureOpts{renderer: r, cfg: codes.Config{BatchWorkers: 3}})
	var items []map[string]any
	for i := 0; i < 12; i++ {
		items = append(items, map[string]any{"destination": fmt.Sprintf("https://example.com/%d", i)})
	}
	start := time.Now()
	res, results, _ := f.batch(t, "alice", items)
	elapsed := time.Since(start)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("batch: %d", res.StatusCode)
	}
	for i, r := range results {
		if r["status"] != "created" {
			t.Fatalf("item %d: %v", i, r)
		}
	}
	if r.calls.Load() != 12 {
		t.Fatalf("%d renders, want 12", r.calls.Load())
	}
	if peak := r.peak.Load(); peak != 3 {
		t.Fatalf("peak concurrency %d, want exactly the 3 configured workers", peak)
	}
	// 12 renders of 20ms on 3 workers is ~80ms; serial would be 240ms.
	if elapsed > 200*time.Millisecond {
		t.Fatalf("batch took %s; rendering does not look parallel", elapsed)
	}
	if n := f.countCodes(t, "alice"); n != 12 {
		t.Fatalf("%d codes, want 12", n)
	}
}
