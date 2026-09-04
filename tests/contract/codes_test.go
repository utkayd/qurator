package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/utkayd/qurator/internal/blob"
	"github.com/utkayd/qurator/internal/blob/blobtest"
	"github.com/utkayd/qurator/internal/codes"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi"
	"github.com/utkayd/qurator/internal/httpapi/public"
	v1 "github.com/utkayd/qurator/internal/httpapi/v1"
	"github.com/utkayd/qurator/internal/store"
	"github.com/utkayd/qurator/internal/store/storetest"
)

const (
	testBaseURL = "https://qr.example.com"
	userHeader  = "X-Test-User" // stands in for the auth middleware's identity
)

// fakeRenderer stands in for Stream A's renderer: a deterministic PNG-ish payload that
// embeds the content so a test can prove the encoded value is the scan URL.
type fakeRenderer struct{}

func (fakeRenderer) Render(_ context.Context, content string, _ domain.Styling) ([]byte, error) {
	return []byte("\x89PNG\r\n\x1a\n" + content), nil
}

type fixture struct {
	store    store.Store
	blob     blob.BlobStore
	cache    *codes.Cache
	svc      *codes.Service
	handler  http.Handler
	recorder *captureRecorder
	users    map[string]string // label -> user id
}

type captureRecorder struct {
	events []domain.ScanEvent
}

func (c *captureRecorder) Record(ev domain.ScanEvent) { c.events = append(c.events, ev) }

func identityFromHeader(r *http.Request) (string, bool) {
	id := r.Header.Get(userHeader)
	return id, id != ""
}

func newFixture(t *testing.T, st store.Store, fallback string) *fixture {
	t.Helper()
	if st == nil {
		st = storetest.NewMemStore()
	}
	bl := blobtest.NewMemBlob()
	cache := codes.NewCache()
	svc := codes.NewService(st, bl, fakeRenderer{}, cache, codes.Config{
		BaseURL:        testBaseURL,
		AllowedSchemes: []string{"http", "https"},
	})
	rec := &captureRecorder{}
	pub := public.NewPublicHandler(public.Options{
		Resolver:            svc,
		Blob:                bl,
		Recorder:            rec,
		FallbackDestination: fallback,
	})
	h := httpapi.NewRouter(httpapi.Handlers{
		Codes:  v1.NewCodesHandler(svc, identityFromHeader),
		Public: pub,
	}, httpapi.Options{})
	f := &fixture{store: st, blob: bl, cache: cache, svc: svc, handler: h, recorder: rec, users: map[string]string{}}
	for _, label := range []string{"alice", "bob"} {
		u := &domain.User{ID: domain.NewUserID(), Email: label + "@example.com", Source: domain.UserSourceLocal}
		if err := st.CreateUser(context.Background(), u); err != nil {
			t.Fatalf("create user %s: %v", label, err)
		}
		f.users[label] = u.ID
	}
	return f
}

func (f *fixture) do(t *testing.T, user, method, path string, body any, hdr map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		req.Header.Set(userHeader, f.users[user])
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	f.handler.ServeHTTP(rr, req)
	res := rr.Result()
	var out map[string]any
	if strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		raw, _ := io.ReadAll(res.Body)
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatalf("%s %s: invalid JSON body %q: %v", method, path, raw, err)
			}
		}
	}
	return res, out
}

func errCodeCodes(t *testing.T, body map[string]any) (string, map[string]any) {
	t.Helper()
	e, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error envelope in %v", body)
	}
	details, _ := e["details"].(map[string]any)
	return e["code"].(string), details
}

func (f *fixture) create(t *testing.T, user string, req map[string]any) map[string]any {
	t.Helper()
	res, body := f.do(t, user, http.MethodPost, "/v1/codes", req, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("POST /v1/codes %v: status %d body %v", req, res.StatusCode, body)
	}
	return body
}

func TestCodes_CreateGeneratedAndAlias(t *testing.T) {
	f := newFixture(t, nil, "")

	gen := f.create(t, "alice", map[string]any{"destination": "https://example.com/a"})
	sc := gen["short_code"].(string)
	if len(sc) != 12 || gen["is_alias"] != false {
		t.Fatalf("generated code: short_code=%q is_alias=%v", sc, gen["is_alias"])
	}
	if gen["version"] != float64(1) || gen["state"] != "active" {
		t.Fatalf("generated code: version=%v state=%v", gen["version"], gen["state"])
	}
	id := gen["id"].(string)
	if gen["scan_url"] != testBaseURL+"/r/"+sc {
		t.Fatalf("scan_url = %v", gen["scan_url"])
	}
	if gen["image_url"] != testBaseURL+"/i/"+id+".png" {
		t.Fatalf("image_url = %v", gen["image_url"])
	}
	styling, _ := gen["styling"].(map[string]any)
	for _, k := range []string{"fg_color", "bg_color", "module_shape", "margin_modules", "size_px", "ec_level", "ec_level_effective"} {
		if _, ok := styling[k]; !ok {
			t.Fatalf("styling missing %q: %v", k, styling)
		}
	}

	// The persisted image encodes the scan URL, never the destination (FR-007, FR-010).
	rc, _, err := f.blob.Get(context.Background(), codes.BlobKeyFor(id))
	if err != nil {
		t.Fatalf("image blob missing: %v", err)
	}
	raw, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Contains(raw, []byte(testBaseURL+"/r/"+sc)) || bytes.Contains(raw, []byte("example.com/a")) {
		t.Fatalf("image payload encodes %q, want the scan URL only", raw)
	}

	al := f.create(t, "alice", map[string]any{"destination": "https://example.com/b", "alias": "Spring-Sale", "styling": map[string]any{"module_shape": "rounded", "size_px": 1024, "ec_level": "Q"}})
	if al["short_code"] != "spring-sale" || al["is_alias"] != true {
		t.Fatalf("alias code: %v", al)
	}
	if st := al["styling"].(map[string]any); st["module_shape"] != "rounded" || st["size_px"] != float64(1024) || st["ec_level"] != "Q" {
		t.Fatalf("styling not persisted: %v", st)
	}

	// GET round-trips the same representation.
	res, got := f.do(t, "alice", http.MethodGet, "/v1/codes/"+al["id"].(string), nil, nil)
	if res.StatusCode != http.StatusOK || got["short_code"] != "spring-sale" {
		t.Fatalf("GET: %d %v", res.StatusCode, got)
	}
}

func TestCodes_AliasConflicts(t *testing.T) {
	f := newFixture(t, nil, "")
	first := f.create(t, "alice", map[string]any{"destination": "https://example.com/", "alias": "spring-sale"})

	res, body := f.do(t, "alice", http.MethodPost, "/v1/codes", map[string]any{"destination": "https://example.com/", "alias": "SPRING-sale"}, nil)
	if code, details := errCodeCodes(t, body); res.StatusCode != http.StatusConflict || code != "alias_taken" || details["alias"] != "spring-sale" {
		t.Fatalf("case-variant alias: %d %v", res.StatusCode, body)
	}

	// Deleting the owner does not free the alias (FR-018).
	if res, _ := f.do(t, "alice", http.MethodDelete, "/v1/codes/"+first["id"].(string), nil, nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE: %d", res.StatusCode)
	}
	res, body = f.do(t, "bob", http.MethodPost, "/v1/codes", map[string]any{"destination": "https://example.com/", "alias": "spring-sale"}, nil)
	if code, _ := errCodeCodes(t, body); res.StatusCode != http.StatusConflict || code != "alias_taken" {
		t.Fatalf("alias of deleted code: %d %v", res.StatusCode, body)
	}

	res, body = f.do(t, "alice", http.MethodPost, "/v1/codes", map[string]any{"destination": "https://example.com/", "alias": "healthz"}, nil)
	if code, details := errCodeCodes(t, body); res.StatusCode != http.StatusConflict || code != "alias_reserved" || details["alias"] != "healthz" {
		t.Fatalf("reserved alias: %d %v", res.StatusCode, body)
	}

	for _, bad := range []string{"abcdefghjkmn", "ab", "-lead", "dou--ble", "has space", strings.Repeat("a", 65)} {
		res, body = f.do(t, "alice", http.MethodPost, "/v1/codes", map[string]any{"destination": "https://example.com/", "alias": bad}, nil)
		if code, details := errCodeCodes(t, body); res.StatusCode != http.StatusBadRequest || code != "alias_invalid" || details["reason"] == nil {
			t.Fatalf("invalid alias %q: %d %v", bad, res.StatusCode, body)
		}
	}
}

func TestCodes_DestinationValidation(t *testing.T) {
	f := newFixture(t, nil, "")

	res, body := f.do(t, "alice", http.MethodPost, "/v1/codes", map[string]any{"destination": "javascript:alert(1)"}, nil)
	if code, details := errCodeCodes(t, body); res.StatusCode != http.StatusBadRequest || code != "unsupported_scheme" || details["scheme"] != "javascript" {
		t.Fatalf("javascript scheme: %d %v", res.StatusCode, body)
	}
	if _, details := errCodeCodes(t, body); details["allowed"] == nil {
		t.Fatalf("unsupported_scheme must list allowed schemes: %v", body)
	}

	selfRefs := []string{
		testBaseURL + "/r/other",
		"https://QR.EXAMPLE.COM/r/other",
		"https://qr.example.com:443/r/other",
		"https://qr.example.com/%72/other",
		"https://qr.example.com/x/../r/other",
		"//qr.example.com/r/other",
		"/r/other",
	}
	for _, dest := range selfRefs {
		res, body := f.do(t, "alice", http.MethodPost, "/v1/codes", map[string]any{"destination": dest}, nil)
		if code, _ := errCodeCodes(t, body); res.StatusCode != http.StatusBadRequest || code != "self_referential_destination" {
			t.Fatalf("self-ref %q: %d %v", dest, res.StatusCode, body)
		}
	}
	// Same host but not the scan path is fine.
	f.create(t, "alice", map[string]any{"destination": testBaseURL + "/ui/"})

	res, body = f.do(t, "alice", http.MethodPost, "/v1/codes", map[string]any{}, nil)
	if code, _ := errCodeCodes(t, body); res.StatusCode != http.StatusBadRequest || code != "invalid_request" {
		t.Fatalf("missing destination: %d %v", res.StatusCode, body)
	}
	res, body = f.do(t, "alice", http.MethodPost, "/v1/codes", map[string]any{"destination": "https://example.com/", "extra": 1}, nil)
	if code, _ := errCodeCodes(t, body); res.StatusCode != http.StatusBadRequest || code != "invalid_request" {
		t.Fatalf("unknown field: %d %v", res.StatusCode, body)
	}

	// PATCH re-validates (FR-011 on every update).
	c := f.create(t, "alice", map[string]any{"destination": "https://example.com/ok"})
	res, body = f.do(t, "alice", http.MethodPatch, "/v1/codes/"+c["id"].(string), map[string]any{"destination": "ftp://example.com/"}, nil)
	if code, _ := errCodeCodes(t, body); res.StatusCode != http.StatusBadRequest || code != "unsupported_scheme" {
		t.Fatalf("PATCH ftp: %d %v", res.StatusCode, body)
	}
	_, got := f.do(t, "alice", http.MethodGet, "/v1/codes/"+c["id"].(string), nil, nil)
	if got["destination"] != "https://example.com/ok" {
		t.Fatalf("rejected PATCH mutated destination: %v", got["destination"])
	}
}

func TestCodes_ListPaginationStable(t *testing.T) {
	f := newFixture(t, nil, "")
	ids := map[string]bool{}
	for i := 0; i < 5; i++ {
		c := f.create(t, "alice", map[string]any{"destination": fmt.Sprintf("https://example.com/%d", i)})
		ids[c["id"].(string)] = true
	}
	f.create(t, "bob", map[string]any{"destination": "https://example.com/bobs"})

	seen := map[string]int{}
	cursor := ""
	for page := 0; ; page++ {
		path := "/v1/codes?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		res, body := f.do(t, "alice", http.MethodGet, path, nil, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("list page %d: %d %v", page, res.StatusCode, body)
		}
		items := body["items"].([]any)
		if len(items) > 2 {
			t.Fatalf("page %d has %d items", page, len(items))
		}
		for _, it := range items {
			seen[it.(map[string]any)["id"].(string)]++
		}
		if page == 0 {
			// Insert mid-listing: must not duplicate or skip existing rows.
			f.create(t, "alice", map[string]any{"destination": "https://example.com/mid"})
		}
		next, _ := body["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = next
		if page > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	for id := range ids {
		if seen[id] != 1 {
			t.Fatalf("code %s seen %d times", id, seen[id])
		}
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("code %s seen %d times", id, n)
		}
	}

	res, body := f.do(t, "alice", http.MethodGet, "/v1/codes?destination_contains=MID", nil, nil)
	if res.StatusCode != http.StatusOK || len(body["items"].([]any)) != 1 {
		t.Fatalf("destination filter: %d %v", res.StatusCode, body)
	}
	res, body = f.do(t, "alice", http.MethodGet, "/v1/codes?limit=0", nil, nil)
	if code, _ := errCodeCodes(t, body); res.StatusCode != http.StatusBadRequest || code != "invalid_request" {
		t.Fatalf("limit=0: %d %v", res.StatusCode, body)
	}
	res, body = f.do(t, "alice", http.MethodGet, "/v1/codes?cursor=not*base64url", nil, nil)
	if code, _ := errCodeCodes(t, body); res.StatusCode != http.StatusBadRequest || code != "invalid_request" {
		t.Fatalf("bad cursor: %d %v", res.StatusCode, body)
	}
}

func TestCodes_PatchIfMatchAndLifecycle(t *testing.T) {
	f := newFixture(t, nil, "")
	c := f.create(t, "alice", map[string]any{"destination": "https://example.com/v1"})
	id := c["id"].(string)

	res, body := f.do(t, "alice", http.MethodPatch, "/v1/codes/"+id, map[string]any{"destination": "https://example.com/v2"}, map[string]string{"If-Match": `"1"`})
	if res.StatusCode != http.StatusOK || body["version"] != float64(2) || body["destination"] != "https://example.com/v2" {
		t.Fatalf("PATCH If-Match 1: %d %v", res.StatusCode, body)
	}
	res, body = f.do(t, "alice", http.MethodPatch, "/v1/codes/"+id, map[string]any{"destination": "https://example.com/v3"}, map[string]string{"If-Match": `"1"`})
	code, details := errCodeCodes(t, body)
	if res.StatusCode != http.StatusConflict || code != "conflict" || details["actual"] != float64(2) || details["expected"] != float64(1) {
		t.Fatalf("stale If-Match: %d %v", res.StatusCode, body)
	}
	res, body = f.do(t, "alice", http.MethodPatch, "/v1/codes/"+id, map[string]any{"destination": "https://example.com/v3"}, map[string]string{"If-Match": `abc`})
	if code, _ := errCodeCodes(t, body); res.StatusCode != http.StatusBadRequest || code != "invalid_request" {
		t.Fatalf("malformed If-Match: %d %v", res.StatusCode, body)
	}
	// Omitted If-Match is last-write-wins.
	res, body = f.do(t, "alice", http.MethodPatch, "/v1/codes/"+id, map[string]any{"destination": "https://example.com/v4"}, nil)
	if res.StatusCode != http.StatusOK || body["version"] != float64(3) {
		t.Fatalf("PATCH without If-Match: %d %v", res.StatusCode, body)
	}

	res, body = f.do(t, "alice", http.MethodPost, "/v1/codes/"+id+"/disable", nil, nil)
	if res.StatusCode != http.StatusOK || body["state"] != "disabled" || body["version"] != float64(4) {
		t.Fatalf("disable: %d %v", res.StatusCode, body)
	}
	res, body = f.do(t, "alice", http.MethodPost, "/v1/codes/"+id+"/enable", nil, nil)
	if res.StatusCode != http.StatusOK || body["state"] != "active" {
		t.Fatalf("enable: %d %v", res.StatusCode, body)
	}
	if res, _ := f.do(t, "alice", http.MethodDelete, "/v1/codes/"+id, nil, nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	res, body = f.do(t, "alice", http.MethodPost, "/v1/codes/"+id+"/enable", nil, nil)
	if code, _ := errCodeCodes(t, body); res.StatusCode != http.StatusConflict || code != "conflict" {
		t.Fatalf("enable deleted: %d %v", res.StatusCode, body)
	}
	res, body = f.do(t, "alice", http.MethodGet, "/v1/codes/"+id, nil, nil)
	if res.StatusCode != http.StatusOK || body["state"] != "deleted" {
		t.Fatalf("GET deleted: %d %v", res.StatusCode, body)
	}
	if res, body := f.do(t, "alice", http.MethodGet, "/v1/codes", nil, nil); res.StatusCode != http.StatusOK || len(body["items"].([]any)) != 0 {
		t.Fatalf("deleted code still listed: %v", body)
	}
}

func TestCodes_OwnershipIsolation(t *testing.T) {
	f := newFixture(t, nil, "")
	c := f.create(t, "alice", map[string]any{"destination": "https://example.com/mine"})
	id := c["id"].(string)

	checks := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/codes/" + id, nil},
		{http.MethodPatch, "/v1/codes/" + id, map[string]any{"destination": "https://evil.example/"}},
		{http.MethodDelete, "/v1/codes/" + id, nil},
		{http.MethodPost, "/v1/codes/" + id + "/disable", nil},
		{http.MethodPost, "/v1/codes/" + id + "/enable", nil},
	}
	for _, ck := range checks {
		res, body := f.do(t, "bob", ck.method, ck.path, ck.body, nil)
		if code, _ := errCodeCodes(t, body); res.StatusCode != http.StatusNotFound || code != "not_found" {
			t.Fatalf("%s %s as bob: %d %v", ck.method, ck.path, res.StatusCode, body)
		}
	}
	_, got := f.do(t, "alice", http.MethodGet, "/v1/codes/"+id, nil, nil)
	if got["destination"] != "https://example.com/mine" || got["state"] != "active" || got["version"] != float64(1) {
		t.Fatalf("row mutated by non-owner: %v", got)
	}
	res, body := f.do(t, "", http.MethodGet, "/v1/codes/"+id, nil, nil)
	if code, _ := errCodeCodes(t, body); res.StatusCode != http.StatusUnauthorized || code != "unauthorized" {
		t.Fatalf("anonymous: %d %v", res.StatusCode, body)
	}
}
