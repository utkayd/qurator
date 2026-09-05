package contract

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/blob"
	"github.com/utkayd/qurator/internal/blob/blobtest"
	"github.com/utkayd/qurator/internal/codes"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi"
	"github.com/utkayd/qurator/internal/httpapi/public"
	v1 "github.com/utkayd/qurator/internal/httpapi/v1"
	"github.com/utkayd/qurator/internal/store"
	"github.com/utkayd/qurator/internal/store/storetest"

	_ "github.com/utkayd/qurator/internal/blob/fsblob"
)

// fixtureOpts parameterises a fixture for the spec 003 tests: which blob store (the
// in-memory one implements blob.URLer; the filesystem driver does not), the service
// config (url_mode, batch bounds) and whether /i/ is switched off.
type fixtureOpts struct {
	store          store.Store
	blob           blob.BlobStore
	renderer       codes.Renderer
	cfg            codes.Config
	imagesDisabled bool
}

func newFixtureOpts(t *testing.T, o fixtureOpts) *fixture {
	t.Helper()
	if o.store == nil {
		o.store = storetest.NewMemStore()
	}
	if o.blob == nil {
		o.blob = blobtest.NewMemBlob()
	}
	if o.renderer == nil {
		o.renderer = fakeRenderer{}
	}
	if o.cfg.BaseURL == "" {
		o.cfg.BaseURL = testBaseURL
	}
	if o.cfg.AllowedSchemes == nil {
		o.cfg.AllowedSchemes = []string{"http", "https"}
	}
	cache := codes.NewCache()
	svc := codes.NewService(o.store, o.blob, o.renderer, cache, o.cfg)
	rec := &captureRecorder{}
	pub := public.NewPublicHandler(public.Options{Resolver: svc, Blob: o.blob, Recorder: rec, ImagesDisabled: o.imagesDisabled})
	h := httpapi.NewRouter(httpapi.Handlers{Codes: v1.NewCodesHandler(svc, identityFromHeader), Public: pub}, httpapi.Options{})
	f := &fixture{store: o.store, blob: o.blob, cache: cache, svc: svc, handler: h, recorder: rec, users: map[string]string{}}
	for _, label := range []string{"alice", "bob"} {
		u := &domain.User{ID: domain.NewUserID(), Email: label + "@example.com", Source: domain.UserSourceLocal}
		if err := o.store.CreateUser(context.Background(), u); err != nil {
			t.Fatalf("create user %s: %v", label, err)
		}
		f.users[label] = u.ID
	}
	return f
}

// fsBlob opens the real filesystem driver in a temp dir: the one BlobStore that cannot
// address its objects (FR-203).
func fsBlob(t *testing.T) blob.BlobStore {
	t.Helper()
	b, err := blob.Open(t.Context(), "fs", blob.Config{Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// memPresignPrefix is the shape blobtest's memblob gives presigned links.
func memPresignPrefix(id string) string { return "mem://" + codes.BlobKeyFor(id) + "?exp=" }

// eachRepresentation runs check on the code as POST returned it, as GET returns it and as
// it appears in the list, so every representation carries the same URL fields (FR-202).
func eachRepresentation(t *testing.T, f *fixture, created map[string]any, check func(where string, c map[string]any)) {
	t.Helper()
	check("POST", created)
	id := created["id"].(string)
	res, got := f.do(t, "alice", http.MethodGet, "/v1/codes/"+id, nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET: %d %v", res.StatusCode, got)
	}
	check("GET", got)
	res, page := f.do(t, "alice", http.MethodGet, "/v1/codes", nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %v", res.StatusCode, page)
	}
	for _, it := range page["items"].([]any) {
		item := it.(map[string]any)
		if item["id"] == id {
			check("list", item)
			return
		}
	}
	t.Fatalf("code %s not in list", id)
}

// TestStorageURL_InstanceModeDerivesStorageURL pins US1 scenario 1: with the default
// url_mode, image_url is this instance's /i/ route and storage_url is present because
// the blob store can derive one (no public base → presigned).
func TestStorageURL_InstanceModeDerivesStorageURL(t *testing.T) {
	f := newFixtureOpts(t, fixtureOpts{cfg: codes.Config{URLMode: "instance", PresignTTL: time.Hour}})
	c := f.create(t, "alice", map[string]any{"destination": "https://example.com/a"})
	id := c["id"].(string)
	eachRepresentation(t, f, c, func(where string, c map[string]any) {
		if c["image_url"] != testBaseURL+"/i/"+id+".png" {
			t.Fatalf("%s: image_url = %v, want the instance route", where, c["image_url"])
		}
		su, _ := c["storage_url"].(string)
		if !strings.HasPrefix(su, memPresignPrefix(id)) {
			t.Fatalf("%s: storage_url = %q, want a presigned link for the blob key", where, su)
		}
	})
	// An empty url_mode means instance too.
	f2 := newFixtureOpts(t, fixtureOpts{})
	c2 := f2.create(t, "alice", map[string]any{"destination": "https://example.com/a"})
	if c2["image_url"] != testBaseURL+"/i/"+c2["id"].(string)+".png" {
		t.Fatalf("default url_mode: image_url = %v", c2["image_url"])
	}
}

// TestStorageURL_PublicMode pins US1 scenario 2: image_url is <base>/<blob key> and
// storage_url equals it, with the base's trailing slash tolerated.
func TestStorageURL_PublicMode(t *testing.T) {
	f := newFixtureOpts(t, fixtureOpts{cfg: codes.Config{URLMode: "public", PublicBaseURL: "https://cdn.example/codes/"}})
	c := f.create(t, "alice", map[string]any{"destination": "https://example.com/a", "mode": "direct"})
	id := c["id"].(string)
	want := "https://cdn.example/codes/" + codes.BlobKeyFor(id)
	eachRepresentation(t, f, c, func(where string, c map[string]any) {
		if c["image_url"] != want {
			t.Fatalf("%s: image_url = %v, want %q", where, c["image_url"], want)
		}
		if c["storage_url"] != want {
			t.Fatalf("%s: storage_url = %v, want %q", where, c["storage_url"], want)
		}
	})
	if !strings.HasSuffix(want, "/"+id+".png") || strings.Contains(want, "//codes") {
		t.Fatalf("public URL %q is not <base>/<blob key>", want)
	}
}

// TestStorageURL_PresignedMode pins US1 scenario 3: image_url is a signed link for the
// blob key whose expiry is the configured TTL from now, computed per response.
func TestStorageURL_PresignedMode(t *testing.T) {
	f := newFixtureOpts(t, fixtureOpts{cfg: codes.Config{URLMode: "presigned", PresignTTL: 30 * time.Minute}})
	c := f.create(t, "alice", map[string]any{"destination": "https://example.com/a"})
	id := c["id"].(string)
	eachRepresentation(t, f, c, func(where string, c map[string]any) {
		for _, k := range []string{"image_url", "storage_url"} {
			u, _ := c[k].(string)
			if !strings.HasPrefix(u, memPresignPrefix(id)) {
				t.Fatalf("%s: %s = %q, want a presigned link for the blob key", where, k, u)
			}
			exp, err := strconv.ParseInt(strings.TrimPrefix(u, memPresignPrefix(id)), 10, 64)
			if err != nil {
				t.Fatalf("%s: %s expiry: %v", where, k, err)
			}
			if d := time.Until(time.Unix(exp, 0)); d < 29*time.Minute || d > 31*time.Minute {
				t.Fatalf("%s: %s expires in %s, want ~30m", where, k, d)
			}
		}
	})
	// The presigned link is not what the QR encodes and is never stored: the row keeps
	// only the blob key.
	row, err := f.store.GetCodeByID(context.Background(), id, f.users["alice"])
	if err != nil {
		t.Fatal(err)
	}
	if row.BlobKey != codes.BlobKeyFor(id) {
		t.Fatalf("stored BlobKey = %q", row.BlobKey)
	}
}

// TestStorageURL_AbsentWithFilesystemDriver pins US1 scenario 5 / FR-202: the fs driver
// cannot address its files, so storage_url is omitted — not empty, not fabricated.
func TestStorageURL_AbsentWithFilesystemDriver(t *testing.T) {
	f := newFixtureOpts(t, fixtureOpts{blob: fsBlob(t)})
	c := f.create(t, "alice", map[string]any{"destination": "https://example.com/a"})
	id := c["id"].(string)
	eachRepresentation(t, f, c, func(where string, c map[string]any) {
		if _, present := c["storage_url"]; present {
			t.Fatalf("%s: storage_url must be absent with the fs driver, got %v", where, c["storage_url"])
		}
		if c["image_url"] != testBaseURL+"/i/"+id+".png" {
			t.Fatalf("%s: image_url = %v", where, c["image_url"])
		}
	})
	// The image is still served by the instance.
	if res := scan(f, "/i/"+id+".png", nil); res.StatusCode != http.StatusOK {
		t.Fatalf("/i/ with fs driver: %d", res.StatusCode)
	}
}

// TestImage_404WhenServingDisabled pins US2 scenario 1 / FR-204: with serving switched
// off every /i/ request is a JSON 404, the route needs no auth to say so, and code
// responses still carry a storage_url the operator can hand out.
func TestImage_404WhenServingDisabled(t *testing.T) {
	f := newFixtureOpts(t, fixtureOpts{
		cfg:            codes.Config{URLMode: "public", PublicBaseURL: "https://cdn.example"},
		imagesDisabled: true,
	})
	c := f.create(t, "alice", map[string]any{"destination": "https://example.com/a"})
	id := c["id"].(string)
	if c["storage_url"] != "https://cdn.example/"+codes.BlobKeyFor(id) {
		t.Fatalf("storage_url = %v", c["storage_url"])
	}
	// The blob exists; only the route declines.
	if _, err := f.blob.Stat(context.Background(), codes.BlobKeyFor(id)); err != nil {
		t.Fatalf("image blob missing: %v", err)
	}
	for _, p := range []string{"/i/" + id + ".png", "/i/cod_0000000000000000.png", "/i/whatever"} {
		res := scan(f, p, nil)
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s with serving disabled: %d, want 404", p, res.StatusCode)
		}
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("%s: Content-Type %q, want the JSON envelope", p, ct)
		}
	}
	// The scan path is unaffected.
	assertRedirect(t, scan(f, "/r/"+c["short_code"].(string), nil), "https://example.com/a")

	// And with serving on (the default) the same request succeeds.
	on := newFixtureOpts(t, fixtureOpts{})
	c2 := on.create(t, "alice", map[string]any{"destination": "https://example.com/a"})
	if res := scan(on, "/i/"+c2["id"].(string)+".png", nil); res.StatusCode != http.StatusOK {
		t.Fatalf("/i/ with serving enabled: %d", res.StatusCode)
	}
}
