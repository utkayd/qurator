// Package contract holds HTTP-level tests pinned to contracts/openapi.yaml. They exercise
// the real router and handlers against the in-memory store; no network, no Docker.
package contract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/analytics"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi"
	v1 "github.com/utkayd/qurator/internal/httpapi/v1"
	"github.com/utkayd/qurator/internal/store"
	"github.com/utkayd/qurator/internal/store/storetest"
)

const (
	ownerID    = "usr_owner"
	strangerID = "usr_stranger"
	codeID     = "cod_a1b2c3d4e5f6g7h8"
)

// fixture: one code with a known set of scans spread over several days and hours.
func analyticsFixture(t *testing.T) (http.Handler, store.Store, time.Time) {
	t.Helper()
	ctx := context.Background()
	ms := storetest.NewMemStore()
	for _, u := range []*domain.User{{ID: ownerID, Email: "o@example.com"}, {ID: strangerID, Email: "s@example.com"}} {
		if err := ms.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if err := ms.CreateCode(ctx, &domain.Code{ID: codeID, ShortCode: "camp1", UserID: ownerID, Destination: "https://example.com"}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC) // a Monday

	var events []domain.ScanEvent
	add := func(at time.Time, fam string, dev domain.DeviceCategory, ref string, bot bool) {
		events = append(events, domain.ScanEvent{CodeID: codeID, OccurredAt: at, UAFamily: fam, DeviceCategory: dev, ReferrerHost: ref, IsBot: bot})
	}
	// Day 0: 3 scans in hour 9, 2 in hour 15.
	add(base.Add(9*time.Hour), "Chrome", domain.DeviceDesktop, "", false)
	add(base.Add(9*time.Hour+5*time.Minute), "Safari", domain.DeviceMobile, "instagram.com", false)
	add(base.Add(9*time.Hour+30*time.Minute), "Safari", domain.DeviceMobile, "instagram.com", false)
	add(base.Add(15*time.Hour), "unknown", domain.DeviceBot, "", true)
	add(base.Add(15*time.Hour+1*time.Minute), "Firefox", domain.DeviceDesktop, "t.co", false)
	// Day 2: 4 scans.
	for i := 0; i < 4; i++ {
		add(base.Add(2*24*time.Hour+time.Duration(i)*time.Hour), "Chrome", domain.DeviceMobile, "", false)
	}
	// Day 9 (next week): 1 scan.
	add(base.Add(9*24*time.Hour+12*time.Hour), "Chrome", domain.DeviceTablet, "", false)

	if err := ms.InsertScanBatch(ctx, domain.ScanBatch{Events: events, Rollups: analytics.BuildRollups(events)}); err != nil {
		t.Fatal(err)
	}

	identity := func(r *http.Request) (string, bool) {
		u := r.Header.Get("X-Test-User")
		return u, u != ""
	}
	h := v1.NewAnalyticsHandler(ms, identity)
	h.Now = func() time.Time { return base.Add(20 * 24 * time.Hour) }
	router := httpapi.NewRouter(httpapi.Handlers{Analytics: h}, httpapi.Options{})
	return router, ms, base
}

type analyticsBody struct {
	CodeID string `json:"code_id"`
	From   string `json:"from"`
	To     string `json:"to"`
	Total  int64  `json:"total"`
	Series []struct {
		BucketStart string `json:"bucket_start"`
		Count       int64  `json:"count"`
	} `json:"series"`
	Breakdowns map[string][]struct {
		Value string `json:"value"`
		Count int64  `json:"count"`
	} `json:"breakdowns"`
}

func get(t *testing.T, h http.Handler, user, query string) (*httptest.ResponseRecorder, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/codes/"+codeID+"/analytics"+query, nil)
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr, rr.Body.Bytes()
}

func decodeAnalytics(t *testing.T, rr *httptest.ResponseRecorder, raw []byte) analyticsBody {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, raw)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type %q", ct)
	}
	var b analyticsBody
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	return b
}

func assertSums(t *testing.T, b analyticsBody) {
	t.Helper()
	for _, dim := range []string{"ua_family", "device_category", "referrer_host", "is_bot"} {
		rows, ok := b.Breakdowns[dim]
		if !ok {
			t.Fatalf("breakdowns missing dimension %q", dim)
		}
		var sum int64
		for _, r := range rows {
			sum += r.Count
		}
		if sum != b.Total {
			t.Fatalf("dimension %s sums to %d, total %d", dim, sum, b.Total)
		}
	}
	var series int64
	for _, p := range b.Series {
		series += p.Count
	}
	if series != b.Total {
		t.Fatalf("series sums to %d, total %d", series, b.Total)
	}
	if len(b.Breakdowns) != 4 {
		t.Fatalf("breakdowns has %d keys, want exactly the 4 AnalyticsDimension values: %v", len(b.Breakdowns), b.Breakdowns)
	}
}

func TestAnalyticsResponseShape(t *testing.T) {
	h, _, base := analyticsFixture(t)
	from, to := base.Format(time.RFC3339), base.Add(14*24*time.Hour).Format(time.RFC3339)
	rr, raw := get(t, h, ownerID, "?from="+from+"&to="+to)
	b := decodeAnalytics(t, rr, raw)

	// CodeAnalytics: additionalProperties: false, required [code_id, from, to, total, series, breakdowns].
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"code_id": true, "from": true, "to": true, "total": true, "series": true, "breakdowns": true}
	for k := range top {
		if !want[k] {
			t.Fatalf("unexpected top-level key %q (schema forbids additional properties)", k)
		}
	}
	for k := range want {
		if _, ok := top[k]; !ok {
			t.Fatalf("missing required key %q", k)
		}
	}
	if b.CodeID != codeID {
		t.Fatalf("code_id=%q", b.CodeID)
	}
	if _, err := time.Parse(time.RFC3339, b.From); err != nil {
		t.Fatalf("from is not date-time: %q", b.From)
	}
	if b.Total != 10 {
		t.Fatalf("total=%d, want 10", b.Total)
	}
	assertSums(t, b)
	for _, p := range b.Series {
		if _, err := time.Parse(time.RFC3339, p.BucketStart); err != nil {
			t.Fatalf("bucket_start not date-time: %q", p.BucketStart)
		}
	}
	if b.Breakdowns["is_bot"][0].Value != "false" || b.Breakdowns["is_bot"][0].Count != 9 {
		t.Fatalf("is_bot breakdown: %+v", b.Breakdowns["is_bot"])
	}
}

func TestAnalyticsTimeRangeFiltering(t *testing.T) {
	h, _, base := analyticsFixture(t)
	// Only day 0, hour 9 → [09:00, 10:00): 3 scans.
	rr, raw := get(t, h, ownerID, "?from="+base.Add(9*time.Hour).Format(time.RFC3339)+"&to="+base.Add(10*time.Hour).Format(time.RFC3339)+"&bucket=hour")
	b := decodeAnalytics(t, rr, raw)
	if b.Total != 3 {
		t.Fatalf("hour 9 total=%d, want 3", b.Total)
	}
	assertSums(t, b)
	// Days 0..1 → 5 scans; day 2's scans are excluded by the half-open upper bound.
	rr, raw = get(t, h, ownerID, "?from="+base.Format(time.RFC3339)+"&to="+base.Add(2*24*time.Hour).Format(time.RFC3339))
	b = decodeAnalytics(t, rr, raw)
	if b.Total != 5 {
		t.Fatalf("days 0-1 total=%d, want 5", b.Total)
	}
	// Empty range → zero total, empty (not null) series, four empty breakdowns.
	rr, raw = get(t, h, ownerID, "?from=2020-01-01T00:00:00Z&to=2020-02-01T00:00:00Z")
	b = decodeAnalytics(t, rr, raw)
	if b.Total != 0 || b.Series == nil || len(b.Series) != 0 {
		t.Fatalf("empty range: %s", raw)
	}
	assertSums(t, b)
	if !strings.Contains(string(raw), `"series":[]`) {
		t.Fatalf("empty series must serialise as [] not null: %s", raw)
	}
}

func TestAnalyticsBuckets(t *testing.T) {
	h, _, base := analyticsFixture(t)
	from, to := base.Format(time.RFC3339), base.Add(14*24*time.Hour).Format(time.RFC3339)

	cases := []struct {
		bucket string
		points int
		first  string
	}{
		{"hour", 7, base.Add(9 * time.Hour).Format(time.RFC3339)},
		{"day", 3, base.Format(time.RFC3339)},
		{"week", 2, base.Format(time.RFC3339)}, // base is a Monday: weeks start Monday UTC
		{"", 3, base.Format(time.RFC3339)},     // default is day
	}
	for _, tc := range cases {
		q := "?from=" + from + "&to=" + to
		if tc.bucket != "" {
			q += "&bucket=" + tc.bucket
		}
		rr, raw := get(t, h, ownerID, q)
		b := decodeAnalytics(t, rr, raw)
		if len(b.Series) != tc.points {
			t.Fatalf("bucket=%q: %d series points, want %d: %s", tc.bucket, len(b.Series), tc.points, raw)
		}
		if b.Series[0].BucketStart != tc.first {
			t.Fatalf("bucket=%q: first bucket_start %q, want %q", tc.bucket, b.Series[0].BucketStart, tc.first)
		}
		assertSums(t, b)
	}
	rr, raw := get(t, h, ownerID, "?from="+from+"&to="+to+"&bucket=week")
	if b := decodeAnalytics(t, rr, raw); b.Series[0].Count != 9 || b.Series[1].Count != 1 {
		t.Fatalf("week counts: %+v", b.Series)
	}
}

func TestAnalyticsDefaultsToLast30Days(t *testing.T) {
	h, _, base := analyticsFixture(t)
	rr, raw := get(t, h, ownerID, "")
	b := decodeAnalytics(t, rr, raw)
	if b.Total != 10 {
		t.Fatalf("default range should cover the fixture (now = base+20d): total=%d", b.Total)
	}
	to, _ := time.Parse(time.RFC3339, b.To)
	from, _ := time.Parse(time.RFC3339, b.From)
	if d := to.Sub(from); d != 30*24*time.Hour {
		t.Fatalf("default range is %v, want 30 days", d)
	}
	if !to.Equal(base.Add(20 * 24 * time.Hour)) {
		t.Fatalf("default to=%v, want now", to)
	}
}

func TestAnalyticsValidation(t *testing.T) {
	h, _, base := analyticsFixture(t)
	from := base.Format(time.RFC3339)
	cases := map[string]string{
		"bad from":       "?from=yesterday&to=" + from,
		"bad to":         "?from=" + from + "&to=2026-13-01",
		"from == to":     "?from=" + from + "&to=" + from,
		"from > to":      "?from=" + base.Add(time.Hour).Format(time.RFC3339) + "&to=" + from,
		"bad bucket":     "?from=" + from + "&to=" + base.Add(time.Hour).Format(time.RFC3339) + "&bucket=month",
		"range too wide": "?from=" + from + "&to=" + base.Add(367*24*time.Hour).Format(time.RFC3339),
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			rr, raw := get(t, h, ownerID, q)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400: %s", rr.Code, raw)
			}
			var e httpapi.ErrorBody
			if err := json.Unmarshal(raw, &e); err != nil || e.Error.Code != httpapi.CodeInvalidRequest {
				t.Fatalf("error envelope: %s (err=%v)", raw, err)
			}
		})
	}
	// Exactly 366 days is allowed.
	rr, raw := get(t, h, ownerID, "?from="+from+"&to="+base.Add(366*24*time.Hour).Format(time.RFC3339))
	if rr.Code != http.StatusOK {
		t.Fatalf("366-day range: status %d: %s", rr.Code, raw)
	}
}

func TestAnalyticsOwnershipAndAuth(t *testing.T) {
	h, _, base := analyticsFixture(t)
	q := "?from=" + base.Format(time.RFC3339) + "&to=" + base.Add(24*time.Hour).Format(time.RFC3339)

	rr, raw := get(t, h, strangerID, q)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("another user's code: status %d, want 404 (never an existence oracle): %s", rr.Code, raw)
	}
	var e httpapi.ErrorBody
	if err := json.Unmarshal(raw, &e); err != nil || e.Error.Code != httpapi.CodeNotFound {
		t.Fatalf("error envelope: %s", raw)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/codes/cod_doesnotexist0000/analytics"+q, nil)
	req.Header.Set("X-Test-User", ownerID)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown code: status %d, want 404", rr.Code)
	}

	rr, raw = get(t, h, "", q)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no identity: status %d, want 401: %s", rr.Code, raw)
	}
}

// FR-022 / FR-025: walk every key in the response; none may hint at an address or a place.
func TestAnalyticsNoAddressOrGeographyKeys(t *testing.T) {
	h, _, base := analyticsFixture(t)
	rr, raw := get(t, h, ownerID, "?from="+base.Format(time.RFC3339)+"&to="+base.Add(14*24*time.Hour).Format(time.RFC3339)+"&bucket=hour")
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, raw)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	forbidden := []string{"ip", "addr", "geo", "country", "city", "region", "location", "lat", "lon"}
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				lk := strings.ToLower(k)
				for _, f := range forbidden {
					if strings.Contains(lk, f) {
						t.Errorf("forbidden key %q at %s/%s", k, path, k)
					}
				}
				walk(path+"/"+k, child)
			}
		case []any:
			for i, child := range x {
				walk(path+"/"+strings.Repeat("#", 1)+itoa(i), child)
			}
		}
	}
	walk("", doc)
	// Breakdown dimension names are the closed AnalyticsDimension enum.
	var b analyticsBody
	_ = json.Unmarshal(raw, &b)
	for dim := range b.Breakdowns {
		switch dim {
		case "ua_family", "device_category", "referrer_host", "is_bot":
		default:
			t.Errorf("unexpected dimension %q", dim)
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[n:])
}

// TestAnalyticsDirectCodeNotTracked pins FR-105: analytics on a direct code is a stable
// not_tracked refusal, never an empty aggregate that looks like "nobody scanned it".
func TestAnalyticsDirectCodeNotTracked(t *testing.T) {
	h, ms, base := analyticsFixture(t)
	const directID = "cod_direct0000000001"
	if err := ms.CreateCode(context.Background(), &domain.Code{ID: directID, ShortCode: "printed", UserID: ownerID, Destination: "https://example.com/p", Mode: domain.ModeDirect}); err != nil {
		t.Fatal(err)
	}
	from, to := base.Format(time.RFC3339), base.Add(14*24*time.Hour).Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodGet, "/v1/codes/"+directID+"/analytics?from="+from+"&to="+to, nil)
	req.Header.Set("X-Test-User", ownerID)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.Bytes())
	}
	var env struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v\n%s", err, rr.Body.Bytes())
	}
	if env.Error.Code != "not_tracked" || env.Error.Details["mode"] != "direct" {
		t.Fatalf("want not_tracked with details.mode=direct, got %s", rr.Body.Bytes())
	}
	if strings.Contains(rr.Body.String(), `"total"`) {
		t.Fatalf("not_tracked must not carry an aggregate: %s", rr.Body.Bytes())
	}
	// A stranger still gets 404 before any mode check leaks existence.
	req = httptest.NewRequest(http.MethodGet, "/v1/codes/"+directID+"/analytics?from="+from+"&to="+to, nil)
	req.Header.Set("X-Test-User", strangerID)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("stranger on direct code: %d", rr.Code)
	}
}
