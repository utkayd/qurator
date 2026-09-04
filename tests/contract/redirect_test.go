package contract

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/store"
	"github.com/utkayd/qurator/internal/store/storetest"
)

const (
	wantCacheControl = "no-store, no-cache, must-revalidate"
	imageCacheHeader = "public, max-age=31536000, immutable"
)

// countingStore wraps a Store and counts EVERY call, so the redirect path's one-lookup
// rule (FR-017) is asserted rather than assumed.
type countingStore struct {
	inner store.Store
	calls atomic.Int64
}

func (c *countingStore) reset() int64 { return c.calls.Swap(0) }
func (c *countingStore) hit()         { c.calls.Add(1) }

func (c *countingStore) CreateUser(ctx context.Context, u *domain.User) error {
	c.hit()
	return c.inner.CreateUser(ctx, u)
}
func (c *countingStore) GetUserByID(ctx context.Context, id string) (*domain.User, error) {
	c.hit()
	return c.inner.GetUserByID(ctx, id)
}
func (c *countingStore) GetUserByEmail(ctx context.Context, email string) (*domain.User, error) {
	c.hit()
	return c.inner.GetUserByEmail(ctx, email)
}
func (c *countingStore) BumpTokenVersion(ctx context.Context, userID string) (int64, error) {
	c.hit()
	return c.inner.BumpTokenVersion(ctx, userID)
}
func (c *countingStore) CountUsers(ctx context.Context) (int64, error) {
	c.hit()
	return c.inner.CountUsers(ctx)
}
func (c *countingStore) CreateToken(ctx context.Context, t *domain.APIToken) error {
	c.hit()
	return c.inner.CreateToken(ctx, t)
}
func (c *countingStore) GetTokenByID(ctx context.Context, id string) (*domain.APIToken, error) {
	c.hit()
	return c.inner.GetTokenByID(ctx, id)
}
func (c *countingStore) ListTokens(ctx context.Context, userID string) ([]*domain.APIToken, error) {
	c.hit()
	return c.inner.ListTokens(ctx, userID)
}
func (c *countingStore) RevokeToken(ctx context.Context, id, userID string) error {
	c.hit()
	return c.inner.RevokeToken(ctx, id, userID)
}
func (c *countingStore) TouchTokenLastUsed(ctx context.Context, id string, at time.Time) error {
	c.hit()
	return c.inner.TouchTokenLastUsed(ctx, id, at)
}
func (c *countingStore) CreateCode(ctx context.Context, code *domain.Code) error {
	c.hit()
	return c.inner.CreateCode(ctx, code)
}
func (c *countingStore) GetCodeByShortCode(ctx context.Context, sc string) (*domain.Code, error) {
	c.hit()
	return c.inner.GetCodeByShortCode(ctx, sc)
}
func (c *countingStore) GetCodeByID(ctx context.Context, id, userID string) (*domain.Code, error) {
	c.hit()
	return c.inner.GetCodeByID(ctx, id, userID)
}
func (c *countingStore) ListCodes(ctx context.Context, f domain.CodeFilter) ([]*domain.Code, string, error) {
	c.hit()
	return c.inner.ListCodes(ctx, f)
}
func (c *countingStore) UpdateDestination(ctx context.Context, id, userID, dest string, v int64) error {
	c.hit()
	return c.inner.UpdateDestination(ctx, id, userID, dest, v)
}
func (c *countingStore) SetCodeState(ctx context.Context, id, userID string, s domain.CodeState) error {
	c.hit()
	return c.inner.SetCodeState(ctx, id, userID, s)
}
func (c *countingStore) DeleteCode(ctx context.Context, id, userID string) error {
	c.hit()
	return c.inner.DeleteCode(ctx, id, userID)
}
func (c *countingStore) IsAliasAvailable(ctx context.Context, sc string) (bool, error) {
	c.hit()
	return c.inner.IsAliasAvailable(ctx, sc)
}
func (c *countingStore) ReleaseAlias(ctx context.Context, sc string) error {
	c.hit()
	return c.inner.ReleaseAlias(ctx, sc)
}
func (c *countingStore) InsertScanBatch(ctx context.Context, b domain.ScanBatch) error {
	c.hit()
	return c.inner.InsertScanBatch(ctx, b)
}
func (c *countingStore) QueryAnalytics(ctx context.Context, q domain.AnalyticsQuery) (*domain.AnalyticsResult, error) {
	c.hit()
	return c.inner.QueryAnalytics(ctx, q)
}
func (c *countingStore) PruneScanEvents(ctx context.Context, before time.Time, limit int) (int64, error) {
	c.hit()
	return c.inner.PruneScanEvents(ctx, before, limit)
}
func (c *countingStore) Migrate(ctx context.Context) error { c.hit(); return c.inner.Migrate(ctx) }
func (c *countingStore) Ping(ctx context.Context) error    { c.hit(); return c.inner.Ping(ctx) }
func (c *countingStore) Close() error                      { c.hit(); return c.inner.Close() }

func scan(f *fixture, path string, hdr map[string]string) *http.Response {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	f.handler.ServeHTTP(rr, req)
	return rr.Result()
}

func assertRedirect(t *testing.T, res *http.Response, location string) {
	t.Helper()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("status %d, want 302", res.StatusCode)
	}
	if got := res.Header.Get("Location"); got != location {
		t.Fatalf("Location %q, want %q", got, location)
	}
	if got := res.Header.Get("Cache-Control"); got != wantCacheControl {
		t.Fatalf("Cache-Control %q, want %q", got, wantCacheControl)
	}
	if got := res.Header.Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma %q, want no-cache", got)
	}
}

func assertLanding(t *testing.T, res *http.Response) {
	t.Helper()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200 landing", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type %q, want text/html", ct)
	}
	if got := res.Header.Get("Cache-Control"); got != wantCacheControl {
		t.Fatalf("landing Cache-Control %q, want %q", got, wantCacheControl)
	}
}

func TestRedirect_ActiveCodeUsesOneLookupThenCache(t *testing.T) {
	cs := &countingStore{inner: storetest.NewMemStore()}
	f := newFixture(t, cs, "")
	c := f.create(t, "alice", map[string]any{"destination": "https://example.com/spring", "alias": "spring-sale"})
	cs.reset()

	res := scan(f, "/r/spring-sale", map[string]string{"Referer": "https://news.example/path?x=1", "User-Agent": "Mozilla/5.0"})
	assertRedirect(t, res, "https://example.com/spring")
	if n := cs.reset(); n > 1 {
		t.Fatalf("cold scan made %d store calls, want at most 1", n)
	}
	for _, p := range []string{"/r/spring-sale", "/r/SPRING-SALE", "/r/Spring-Sale"} {
		assertRedirect(t, scan(f, p, nil), "https://example.com/spring")
	}
	if n := cs.reset(); n != 0 {
		t.Fatalf("warm scans made %d store calls, want 0", n)
	}

	if len(f.recorder.events) != 4 {
		t.Fatalf("recorded %d events, want 4", len(f.recorder.events))
	}
	ev := f.recorder.events[0]
	if ev.CodeID != c["id"].(string) || ev.OccurredAt.IsZero() || ev.ReferrerHost != "news.example" {
		t.Fatalf("event %+v", ev)
	}

	// A destination change is visible on the very next scan (same-instance invalidation).
	if res, _ := f.do(t, "alice", http.MethodPatch, "/v1/codes/"+c["id"].(string), map[string]any{"destination": "https://example.com/summer"}, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("PATCH: %d", res.StatusCode)
	}
	assertRedirect(t, scan(f, "/r/spring-sale", nil), "https://example.com/summer")
}

func TestRedirect_LandingForUnknownDisabledDeleted(t *testing.T) {
	f := newFixture(t, nil, "")
	c := f.create(t, "alice", map[string]any{"destination": "https://example.com/x", "alias": "campaign"})
	id := c["id"].(string)

	assertLanding(t, scan(f, "/r/never-existed", nil))

	assertRedirect(t, scan(f, "/r/campaign", nil), "https://example.com/x")
	if res, _ := f.do(t, "alice", http.MethodPost, "/v1/codes/"+id+"/disable", nil, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("disable: %d", res.StatusCode)
	}
	assertLanding(t, scan(f, "/r/campaign", nil))
	if res, _ := f.do(t, "alice", http.MethodPost, "/v1/codes/"+id+"/enable", nil, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("enable: %d", res.StatusCode)
	}
	assertRedirect(t, scan(f, "/r/campaign", nil), "https://example.com/x")
	if res, _ := f.do(t, "alice", http.MethodDelete, "/v1/codes/"+id, nil, nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	assertLanding(t, scan(f, "/r/campaign", nil))
}

func TestRedirect_FallbackDestination(t *testing.T) {
	f := newFixture(t, nil, "https://example.com/fallback")
	c := f.create(t, "alice", map[string]any{"destination": "https://example.com/x", "alias": "campaign"})
	assertRedirect(t, scan(f, "/r/nope", nil), "https://example.com/fallback")
	if res, _ := f.do(t, "alice", http.MethodPost, "/v1/codes/"+c["id"].(string)+"/disable", nil, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("disable: %d", res.StatusCode)
	}
	assertRedirect(t, scan(f, "/r/campaign", nil), "https://example.com/fallback")
}

func TestImage_ServesWithETagAndConditionalRequests(t *testing.T) {
	f := newFixture(t, nil, "")
	c := f.create(t, "alice", map[string]any{"destination": "https://example.com/x"})
	id := c["id"].(string)

	res := scan(f, "/i/"+id+".png", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("image: %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type %q", ct)
	}
	if cc := res.Header.Get("Cache-Control"); cc != imageCacheHeader {
		t.Fatalf("Cache-Control %q", cc)
	}
	etag := res.Header.Get("ETag")
	if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) {
		t.Fatalf("ETag %q not quoted", etag)
	}
	res = scan(f, "/i/"+id+".png", map[string]string{"If-None-Match": etag})
	if res.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match match: %d, want 304", res.StatusCode)
	}
	if got := res.Header.Get("ETag"); got != etag {
		t.Fatalf("304 ETag %q, want %q", got, etag)
	}
	res = scan(f, "/i/"+id+".png", map[string]string{"If-None-Match": `"stale"`})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("If-None-Match mismatch: %d, want 200", res.StatusCode)
	}

	for _, p := range []string{"/i/cod_0000000000000000.png", "/i/" + id + ".gif", "/i/" + id} {
		res := scan(f, p, nil)
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: %d, want 404", p, res.StatusCode)
		}
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("%s: 404 Content-Type %q, want JSON envelope", p, ct)
		}
	}
}

func (c *countingStore) ForEachUser(ctx context.Context, fn func(*domain.User) error) error {
	c.hit()
	return c.inner.ForEachUser(ctx, fn)
}
func (c *countingStore) ForEachCode(ctx context.Context, fn func(*domain.Code) error) error {
	c.hit()
	return c.inner.ForEachCode(ctx, fn)
}
func (c *countingStore) ForEachRollup(ctx context.Context, fn func(domain.RollupDelta) error) error {
	c.hit()
	return c.inner.ForEachRollup(ctx, fn)
}
func (c *countingStore) ForEachReservation(ctx context.Context, fn func(domain.AliasReservation) error) error {
	c.hit()
	return c.inner.ForEachReservation(ctx, fn)
}
