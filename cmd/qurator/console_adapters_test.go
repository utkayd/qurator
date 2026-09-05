package main

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/auth"
	"github.com/utkayd/qurator/internal/blob/blobtest"
	"github.com/utkayd/qurator/internal/codes"
	"github.com/utkayd/qurator/internal/console"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi/middleware"
	"github.com/utkayd/qurator/internal/qr"
	"github.com/utkayd/qurator/internal/store/storetest"
)

// TestConsoleCreatePreservesMode drives the REAL console → adapter → service path. The
// console's own e2e tests use fakes, which is exactly how a dropped field in the adapter
// escaped: the console posted mode=direct and the adapter silently made a dynamic code.
func TestConsoleCreatePreservesMode(t *testing.T) {
	ctx := context.Background()
	st := storetest.NewMemStore()
	bs := blobtest.NewMemBlob()
	if _, err := auth.Bootstrap(ctx, st, "admin@example.com", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	authn, err := auth.New(st, auth.AuthOptions{DevMode: true, SessionTTL: time.Hour}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	svc := codes.NewService(st, bs, codesRenderer{qr.NewRenderer(qr.Bounds{})}, codes.NewCache(),
		codes.Config{BaseURL: "http://qurator.test", AllowedSchemes: []string{"http", "https"}})
	h := authn.Middleware(console.New(newConsoleDeps(svc, authn, st)))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(path string, form url.Values) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set(middleware.CSRFHeader, "test")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}
	if resp := post("/ui/signin", url.Values{"email": {"admin@example.com"}, "password": {"correct-horse-battery"}}); resp.StatusCode/100 != 3 {
		t.Fatalf("signin: status %d", resp.StatusCode)
	}

	create := func(mode, dest string) {
		form := url.Values{"destination": {dest}, "fg_color": {"#000000"}, "bg_color": {"#FFFFFF"},
			"module_shape": {"square"}, "margin_modules": {"4"}, "size_px": {"512"}, "ec_level": {"M"}}
		if mode != "" {
			form.Set("mode", mode)
		}
		if resp := post("/ui/codes", form); resp.StatusCode/100 != 3 {
			t.Fatalf("create mode=%q: status %d", mode, resp.StatusCode)
		}
	}
	create("direct", "https://example.com/direct")
	create("dynamic", "https://example.com/dynamic")
	create("", "https://example.com/default")

	got := map[string]domain.CodeMode{}
	var s = st
	if err := s.ForEachCode(ctx, func(c *domain.Code) error { got[c.Destination] = c.Mode; return nil }); err != nil {
		t.Fatal(err)
	}
	want := map[string]domain.CodeMode{
		"https://example.com/direct":  domain.ModeDirect,
		"https://example.com/dynamic": domain.ModeDynamic,
		"https://example.com/default": domain.ModeDynamic,
	}
	for dest, m := range want {
		if got[dest] != m {
			t.Errorf("%s: stored mode %q, want %q", dest, got[dest], m)
		}
	}
}
