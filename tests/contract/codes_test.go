package contract

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
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
	"github.com/utkayd/qurator/internal/qr"
	"github.com/utkayd/qurator/internal/store"
	"github.com/utkayd/qurator/internal/store/storetest"
	"github.com/utkayd/qurator/tools/qrdecode/decode"
)

const (
	testBaseURL = "https://qr.example.com"
	userHeader  = "X-Test-User" // stands in for the auth middleware's identity
)

// fakeRenderer stands in for Stream A's renderer: a deterministic PNG-ish payload that
// embeds the content so a test can prove the encoded value is the scan URL.
type fakeRenderer struct{}

func (fakeRenderer) Render(_ context.Context, content string, s domain.Styling, _ []byte, _ bool) ([]byte, domain.ECLevel, error) {
	return []byte("\x89PNG\r\n\x1a\n" + content), s.ECLevel, nil
}

// realRenderer is a local copy of cmd/qurator's codesRenderer adapter over the real
// internal/qr renderer, so the logo tests exercise the genuine budget arithmetic.
type realRenderer struct{ r *qr.Renderer }

func (c realRenderer) Render(ctx context.Context, content string, s domain.Styling, logo []byte, autoRaise bool) ([]byte, domain.ECLevel, error) {
	opts := qr.Options{
		Content: []byte(content),
		Format:  qr.FormatPNG,
		FgColor: s.FgColor,
		BgColor: s.BgColor,
		Shape:   qr.ModuleShape(s.ModuleShape),
		Margin:  s.MarginModules,
		SizePx:  s.SizePx,
		ECLevel: qr.ECLevel(s.ECLevel),
	}
	if logo != nil {
		opts.Logo = &qr.Logo{Image: logo, Scale: s.LogoScale, AutoRaise: autoRaise}
	}
	res, err := c.r.Render(ctx, opts)
	if err != nil {
		return nil, "", err
	}
	return res.Bytes, domain.ECLevel(res.ECLevelEffective), nil
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
	return newFixtureWith(t, st, fallback, fakeRenderer{})
}

func newFixtureWith(t *testing.T, st store.Store, fallback string, r codes.Renderer) *fixture {
	t.Helper()
	if st == nil {
		st = storetest.NewMemStore()
	}
	bl := blobtest.NewMemBlob()
	cache := codes.NewCache()
	svc := codes.NewService(st, bl, r, cache, codes.Config{
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
	_ = rc.Close()
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

// logoPNG is a generated 32x32 opaque PNG, base64-encoded for the JSON body.
func logoPNG(t *testing.T) (raw []byte, b64 string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{R: 0x1f, G: 0x6f, B: 0xd0, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestCodes_LogoOnDynamicCode(t *testing.T) {
	renderer := qr.NewRenderer(qr.Bounds{MaxPx: 1024, MaxDuration: 0, MaxPayload: 2953})
	f := newFixtureWith(t, nil, "", realRenderer{renderer})
	rawLogo, b64 := logoPNG(t)

	// Within L's 5% budget: level kept.
	c := f.create(t, "alice", map[string]any{
		"destination": "https://example.com/logo",
		"styling":     map[string]any{"ec_level": "L", "logo": map[string]any{"image_base64": b64, "scale": 0.04}},
	})
	st := c["styling"].(map[string]any)
	if st["ec_level"] != "L" || st["ec_level_effective"] != "L" || st["has_logo"] != true {
		t.Fatalf("scale 0.04 at L: styling %v", st)
	}
	id := c["id"].(string)
	rc, info, err := f.blob.Get(context.Background(), codes.LogoBlobKeyFor(id))
	if err != nil {
		t.Fatalf("logo blob missing: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, rawLogo) {
		t.Fatalf("logo blob is not the original bytes (%d vs %d)", len(got), len(rawLogo))
	}
	if info.ContentType != "image/png" {
		t.Fatalf("logo content type = %q, want image/png", info.ContentType)
	}
	if rc, _, err := f.blob.Get(context.Background(), codes.BlobKeyFor(id)); err != nil {
		t.Fatalf("image blob missing: %v", err)
	} else {
		raw, _ := io.ReadAll(rc)
		_ = rc.Close()
		if _, err := png.Decode(bytes.NewReader(raw)); err != nil {
			t.Fatalf("persisted image is not a PNG: %v", err)
		}
	}
	// The stored row records the effective level and the logo key (FR-027).
	row, err := f.store.GetCodeByID(context.Background(), id, f.users["alice"])
	if err != nil {
		t.Fatal(err)
	}
	if row.Styling.ECLevelEffective != domain.ECLow || row.Styling.LogoBlobKey != codes.LogoBlobKeyFor(id) || row.Styling.LogoScale != 0.04 {
		t.Fatalf("persisted styling: %+v", row.Styling)
	}

	// Over L's budget with auto_raise: raised to the first level that fits (0.22 -> H).
	c = f.create(t, "alice", map[string]any{
		"destination": "https://example.com/logo",
		"styling":     map[string]any{"ec_level": "L", "logo": map[string]any{"image_base64": b64, "scale": 0.22, "auto_raise": true}},
	})
	st = c["styling"].(map[string]any)
	if st["ec_level"] != "L" || st["ec_level_effective"] != "H" {
		t.Fatalf("scale 0.22 at L with auto_raise: styling %v", st)
	}

	// Over budget with auto_raise off: rejected, nothing persisted.
	res, body := f.do(t, "alice", http.MethodPost, "/v1/codes", map[string]any{
		"destination": "https://example.com/logo",
		"styling":     map[string]any{"ec_level": "L", "logo": map[string]any{"image_base64": b64, "scale": 0.22, "auto_raise": false}},
	}, nil)
	code, details := errCodeCodes(t, body)
	if res.StatusCode != http.StatusBadRequest || code != "logo_too_large" {
		t.Fatalf("scale 0.22 at L without auto_raise: %d %v", res.StatusCode, body)
	}
	if details["max_scale"] != 0.05 || details["ec_level"] != "L" || details["scale"] != 0.22 {
		t.Fatalf("logo_too_large details: %v", details)
	}
	if res, body := f.do(t, "alice", http.MethodGet, "/v1/codes", nil, nil); res.StatusCode != http.StatusOK || len(body["items"].([]any)) != 2 {
		t.Fatalf("rejected create left a row behind: %v", body)
	}

	// Malformed logo spec is a schema error, not a renderer error.
	res, body = f.do(t, "alice", http.MethodPost, "/v1/codes", map[string]any{
		"destination": "https://example.com/logo",
		"styling":     map[string]any{"logo": map[string]any{"image_base64": "%%%not-base64"}},
	}, nil)
	if code, details := errCodeCodes(t, body); res.StatusCode != http.StatusBadRequest || code != "invalid_request" || details["field"] != "styling.logo.image_base64" {
		t.Fatalf("bad base64: %d %v", res.StatusCode, body)
	}
}

// ---- feature 002: direct codes ---------------------------------------------------------

// TestCodes_DirectModeEncodesDestination pins FR-101/FR-102/FR-106: a direct code's
// stored PNG decodes (with the independent decoder) to the destination itself, the
// response says so via `mode`, and no scan address is offered for it.
func TestCodes_DirectModeEncodesDestination(t *testing.T) {
	renderer := qr.NewRenderer(qr.Bounds{MaxPx: 1024, MaxDuration: 0, MaxPayload: 2953})
	f := newFixtureWith(t, nil, "", realRenderer{renderer})

	const dest = "https://example.com/direct?campaign=print"
	c := f.create(t, "alice", map[string]any{"destination": dest, "mode": "direct"})
	if c["mode"] != "direct" {
		t.Fatalf("mode = %v, want direct", c["mode"])
	}
	if _, present := c["scan_url"]; present {
		t.Fatalf("direct code must omit scan_url entirely, got %v", c["scan_url"])
	}
	id := c["id"].(string)
	if c["image_url"] != testBaseURL+"/i/"+id+".png" {
		t.Fatalf("image_url = %v", c["image_url"])
	}

	rc, _, err := f.blob.Get(context.Background(), codes.BlobKeyFor(id))
	if err != nil {
		t.Fatalf("image blob missing: %v", err)
	}
	raw, _ := io.ReadAll(rc)
	_ = rc.Close()
	res, err := decode.PNG(raw)
	if err != nil {
		t.Fatalf("decode stored PNG: %v", err)
	}
	if string(res.Bytes) != dest {
		t.Fatalf("direct image decodes to %q, want the destination %q", res.Bytes, dest)
	}

	// GET and list carry the same representation: mode present, scan_url absent.
	for _, path := range []string{"/v1/codes/" + id, "/v1/codes"} {
		r, body := f.do(t, "alice", http.MethodGet, path, nil, nil)
		if r.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d %v", path, r.StatusCode, body)
		}
		item := body
		if items, ok := body["items"].([]any); ok {
			if len(items) != 1 {
				t.Fatalf("list: %d items, want 1", len(items))
			}
			item = items[0].(map[string]any)
		}
		if item["mode"] != "direct" {
			t.Fatalf("GET %s: mode = %v", path, item["mode"])
		}
		if _, present := item["scan_url"]; present {
			t.Fatalf("GET %s: scan_url must be absent for a direct code", path)
		}
	}

	// The short link still resolves (spec 002 edge case): it is a link click, not a scan
	// of the printed image, and behaves like any dynamic code.
	r, _ := f.do(t, "", http.MethodGet, "/r/"+c["short_code"].(string), nil, nil)
	if r.StatusCode != http.StatusFound || r.Header.Get("Location") != dest {
		t.Fatalf("/r/ on direct code: %d Location=%q", r.StatusCode, r.Header.Get("Location"))
	}

	// Styling applies identically (US1 scenario 5).
	styled := f.create(t, "alice", map[string]any{
		"destination": dest, "mode": "direct",
		"styling": map[string]any{"module_shape": "dot", "ec_level": "Q", "fg_color": "#102030"},
	})
	if st := styled["styling"].(map[string]any); st["module_shape"] != "dot" || st["ec_level"] != "Q" || st["fg_color"] != "#102030" {
		t.Fatalf("direct styling not applied: %v", st)
	}
}

// TestCodes_ModeDefaultsToDynamic pins SC-104: omitting mode is byte-for-byte the v1
// behaviour — mode reads back as dynamic, scan_url is present, and the image encodes the
// scan URL rather than the destination.
func TestCodes_ModeDefaultsToDynamic(t *testing.T) {
	f := newFixture(t, nil, "")
	c := f.create(t, "alice", map[string]any{"destination": "https://example.com/default"})
	if c["mode"] != "dynamic" {
		t.Fatalf("mode = %v, want dynamic", c["mode"])
	}
	sc := c["short_code"].(string)
	if c["scan_url"] != testBaseURL+"/r/"+sc {
		t.Fatalf("scan_url = %v", c["scan_url"])
	}
	explicit := f.create(t, "alice", map[string]any{"destination": "https://example.com/explicit", "mode": "dynamic"})
	if explicit["mode"] != "dynamic" || explicit["scan_url"] == nil {
		t.Fatalf("explicit dynamic: %v", explicit)
	}
	res, body := f.do(t, "alice", http.MethodPost, "/v1/codes", map[string]any{"destination": "https://example.com/", "mode": "static"}, nil)
	if code, details := errCodeCodes(t, body); res.StatusCode != http.StatusBadRequest || code != "invalid_request" || details["field"] != "mode" {
		t.Fatalf("unknown mode: %d %v", res.StatusCode, body)
	}
}

// TestCodes_DirectIsImmutable pins FR-104/SC-103: destination updates, disable and
// enable on a direct code are refused with direct_code_immutable, and nothing changes.
func TestCodes_DirectIsImmutable(t *testing.T) {
	f := newFixture(t, nil, "")
	c := f.create(t, "alice", map[string]any{"destination": "https://example.com/printed", "mode": "direct", "alias": "printed-flyer"})
	id := c["id"].(string)

	attempts := []struct {
		name, method, path string
		body               any
		hdr                map[string]string
	}{
		{"patch", http.MethodPatch, "/v1/codes/" + id, map[string]any{"destination": "https://example.com/changed"}, nil},
		{"patch If-Match", http.MethodPatch, "/v1/codes/" + id, map[string]any{"destination": "https://example.com/changed"}, map[string]string{"If-Match": `"1"`}},
		{"patch bad scheme", http.MethodPatch, "/v1/codes/" + id, map[string]any{"destination": "ftp://example.com/"}, nil},
		{"disable", http.MethodPost, "/v1/codes/" + id + "/disable", nil, nil},
		{"enable", http.MethodPost, "/v1/codes/" + id + "/enable", nil, nil},
	}
	for _, a := range attempts {
		res, body := f.do(t, "alice", a.method, a.path, a.body, a.hdr)
		code, details := errCodeCodes(t, body)
		if res.StatusCode != http.StatusConflict || code != "direct_code_immutable" {
			t.Fatalf("%s on direct code: %d %v, want 409 direct_code_immutable", a.name, res.StatusCode, body)
		}
		if details["mode"] != "direct" {
			t.Fatalf("%s: details.mode = %v, want direct", a.name, details["mode"])
		}
		msg, _ := body["error"].(map[string]any)["message"].(string)
		if !strings.Contains(strings.ToLower(msg), "encoded") {
			t.Fatalf("%s: message must say the destination is encoded in the image: %q", a.name, msg)
		}
	}
	_, got := f.do(t, "alice", http.MethodGet, "/v1/codes/"+id, nil, nil)
	if got["destination"] != "https://example.com/printed" || got["state"] != "active" || got["version"] != float64(1) {
		t.Fatalf("direct code mutated by a refused request: %v", got)
	}
	// A non-owner still learns nothing (ownership before mode).
	res, body := f.do(t, "bob", http.MethodPatch, "/v1/codes/"+id, map[string]any{"destination": "https://example.com/x"}, nil)
	if code, _ := errCodeCodes(t, body); res.StatusCode != http.StatusNotFound || code != "not_found" {
		t.Fatalf("PATCH direct as bob: %d %v", res.StatusCode, body)
	}
	// Delete still works (FR-109) and the short code stays reserved (FR-018).
	if res, _ := f.do(t, "alice", http.MethodDelete, "/v1/codes/"+id, nil, nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE direct: %d", res.StatusCode)
	}
	res, body = f.do(t, "alice", http.MethodPost, "/v1/codes", map[string]any{"destination": "https://example.com/", "alias": c["short_code"]}, nil)
	if code, _ := errCodeCodes(t, body); res.StatusCode != http.StatusConflict || code != "alias_taken" {
		t.Fatalf("short code of deleted direct code: %d %v", res.StatusCode, body)
	}
}

// TestCodes_DirectDestinationTooLarge pins FR-103: a direct code's destination must fit
// the symbol at the chosen level, rejected exactly as ephemeral generation rejects it.
func TestCodes_DirectDestinationTooLarge(t *testing.T) {
	renderer := qr.NewRenderer(qr.Bounds{MaxPx: 1024, MaxDuration: 0, MaxPayload: 2953})
	f := newFixtureWith(t, nil, "", realRenderer{renderer})
	long := "https://example.com/" + strings.Repeat("a", 1280) // 1,300 bytes > 1,273 at H

	res, body := f.do(t, "alice", http.MethodPost, "/v1/codes", map[string]any{
		"destination": long, "mode": "direct", "styling": map[string]any{"ec_level": "H"},
	}, nil)
	code, details := errCodeCodes(t, body)
	if res.StatusCode != http.StatusRequestEntityTooLarge || code != "content_too_large" {
		t.Fatalf("1300-byte direct destination at H: %d %v", res.StatusCode, body)
	}
	if details["limit_bytes"] != float64(qr.Capacity(domain.ECHigh)) || details["ec_level"] != "H" {
		t.Fatalf("content_too_large details: %v", details)
	}
	if res, body := f.do(t, "alice", http.MethodGet, "/v1/codes", nil, nil); res.StatusCode != http.StatusOK || len(body["items"].([]any)) != 0 {
		t.Fatalf("rejected direct create left a row behind: %v", body)
	}
	// The same destination fits at L, and a dynamic code never encodes it at all.
	f.create(t, "alice", map[string]any{"destination": long, "mode": "direct", "styling": map[string]any{"ec_level": "L"}})
	f.create(t, "alice", map[string]any{"destination": long, "styling": map[string]any{"ec_level": "H"}})
}
