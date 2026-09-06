package contract

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/utkayd/qurator/internal/blob/blobtest"
	"github.com/utkayd/qurator/internal/codes"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi"
	v1 "github.com/utkayd/qurator/internal/httpapi/v1"
	"github.com/utkayd/qurator/internal/qr"
	"github.com/utkayd/qurator/internal/store/storetest"
	"github.com/utkayd/qurator/tools/qrdecode/decode"
)

type originRenderer struct{ calls int }

func (r *originRenderer) Render(ctx context.Context, content string, s domain.Styling, logo []byte, raise bool) ([]byte, domain.ECLevel, error) {
	r.calls++
	return realRenderer{qr.NewRenderer(qr.Bounds{})}.Render(ctx, content, s, logo, raise)
}

func TestCreationRequiresConfiguredScanOrigin(t *testing.T) {
	ctx := context.Background()
	st := storetest.NewMemStore()
	bl := blobtest.NewMemBlob()
	u := &domain.User{ID: domain.NewUserID(), Email: "origin@example.test"}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	renderer := &originRenderer{}
	svc := codes.NewService(st, bl, renderer, nil, codes.Config{})
	h := httpapi.NewRouter(httpapi.Handlers{Codes: v1.NewCodesHandler(svc, identityFromHeader)}, httpapi.Options{})
	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(userHeader, u.ID)
		r.Host = "attacker.example"
		r.Header.Set("X-Forwarded-Host", "attacker.example")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, body := range []string{`{"destination":"https://example.com"}`, `{"destination":"https://example.com","client_ref":"retry"}`} {
		w := post("/v1/codes", body)
		if w.Code != 503 || !strings.Contains(w.Body.String(), `"scan_url_not_configured"`) {
			t.Errorf("missing origin: %d %s", w.Code, w.Body.String())
		}
	}
	rows, _, err := svc.List(ctx, domain.CodeFilter{UserID: u.ID})
	if err != nil || len(rows) != 0 || renderer.calls != 0 {
		t.Fatalf("rejected creation performed work: rows=%d renders=%d err=%v", len(rows), renderer.calls, err)
	}
	w := post("/v1/codes/batch", `{"items":[{"destination":"https://example.com"},{"mode":"direct","destination":"https://example.com"}]}`)
	if w.Code != 200 {
		t.Fatalf("batch: %d %s", w.Code, w.Body.String())
	}
	var batch struct {
		Results []struct {
			Status string
			Error  struct{ Code string }
			Code   struct{ ID string }
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &batch); err != nil {
		t.Fatal(err)
	}
	if len(batch.Results) != 2 || batch.Results[0].Error.Code != "scan_url_not_configured" || batch.Results[1].Status != "created" {
		t.Fatalf("mixed batch: %s", w.Body.String())
	}
	// Independent decoding verifies direct mode remains useful without an origin.
	id := batch.Results[1].Code.ID
	rc, _, err := bl.Get(ctx, codes.BlobKeyFor(id))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	assertOriginPNG(t, raw, "https://example.com")

	configured := codes.NewService(st, bl, renderer, nil, codes.Config{BaseURL: "https://qr.example/"})
	c, err := configured.Create(ctx, codes.CreateInput{UserID: u.ID, Destination: "https://example.com", ClientRef: "existing"})
	if err != nil {
		t.Fatal(err)
	}
	rc, _, err = bl.Get(ctx, c.BlobKey)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	assertOriginPNG(t, raw, "https://qr.example/r/"+c.ShortCode)
	w = post("/v1/codes", `{"destination":"https://example.com","client_ref":"existing"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), c.ID) {
		t.Fatalf("existing replay: %d %s", w.Code, w.Body.String())
	}
}

func assertOriginPNG(t *testing.T, raw []byte, want string) {
	t.Helper()
	got, err := decode.PNG(raw)
	if err != nil || string(got.Bytes) != want {
		t.Fatalf("decoded %q, want %q: %v", got, want, err)
	}
}
