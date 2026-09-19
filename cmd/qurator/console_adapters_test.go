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
	"github.com/utkayd/qurator/internal/store"
	"github.com/utkayd/qurator/internal/store/storetest"
)

const (
	consoleTestEmail    = "admin@example.com"
	consoleTestPassword = "correct-horse-battery"
)

// consoleTestServer is the REAL console → adapter → service stack over an in-memory
// store, behind the real auth middleware, served over loopback so cookies behave as they
// do in a browser.
type consoleTestServer struct {
	t      *testing.T
	st     store.Store
	authn  *auth.Authenticator
	srv    *httptest.Server
	client *http.Client
}

func newConsoleTestServer(t *testing.T, tune func(*auth.AuthOptions)) *consoleTestServer {
	t.Helper()
	ctx := context.Background()
	st := storetest.NewMemStore()
	bs := blobtest.NewMemBlob()
	if _, err := auth.Bootstrap(ctx, st, consoleTestEmail, consoleTestPassword); err != nil {
		t.Fatal(err)
	}
	opts := auth.AuthOptions{DevMode: true, SessionTTL: time.Hour}
	if tune != nil {
		tune(&opts)
	}
	authn, err := auth.New(st, opts, time.Now)
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
	return &consoleTestServer{t: t, st: st, authn: authn, srv: srv, client: client}
}

// post submits a form with the console's CSRF header and returns the closed response.
func (c *consoleTestServer) post(path string, form url.Values) *http.Response {
	c.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, c.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(middleware.CSRFHeader, "test")
	resp, err := c.client.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp
}

func (c *consoleTestServer) signin() {
	c.t.Helper()
	if resp := c.post("/ui/signin", url.Values{"email": {consoleTestEmail}, "password": {consoleTestPassword}}); resp.StatusCode/100 != 3 {
		c.t.Fatalf("signin: status %d", resp.StatusCode)
	}
}

// sessionCookie returns the console's real session cookie from the jar.
func (c *consoleTestServer) sessionCookie() *http.Cookie {
	c.t.Helper()
	u, _ := url.Parse(c.srv.URL)
	for _, ck := range c.client.Jar.Cookies(u) {
		if ck.Name == auth.SessionCookieName {
			return ck
		}
	}
	c.t.Fatal("no session cookie in jar")
	return nil
}

// TestConsoleCreatePreservesMode drives the REAL console → adapter → service path. The
// console's own e2e tests use fakes, which is exactly how a dropped field in the adapter
// escaped: the console posted mode=direct and the adapter silently made a dynamic code.
func TestConsoleCreatePreservesMode(t *testing.T) {
	ctx := context.Background()
	ts := newConsoleTestServer(t, nil)
	st := ts.st
	post := ts.post
	ts.signin()

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

// TestConsoleSignOutInvalidatesSession drives the real sign-out path (F04): after POST
// /ui/signout the cookie the browser held is refused server-side even if it is replayed,
// because the user's token_version was bumped — not merely because the browser dropped it.
func TestConsoleSignOutInvalidatesSession(t *testing.T) {
	ts := newConsoleTestServer(t, nil)
	ts.signin()
	old := ts.sessionCookie()

	get := func(cookie *http.Cookie) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, ts.srv.URL+"/ui/tokens", nil)
		req.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
		resp, err := http.DefaultTransport.RoundTrip(req) // bypass the jar: replay the old cookie verbatim
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}
	if resp := get(old); resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated page before signout: %d", resp.StatusCode)
	}
	if resp := ts.post("/ui/signout", nil); resp.StatusCode/100 != 3 {
		t.Fatalf("signout: status %d", resp.StatusCode)
	}
	// The auth middleware refuses the stale cookie outright (401 + a clearing Set-Cookie),
	// exactly as it treats any invalid session; the page is never served.
	resp := get(old)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed cookie after signout: %d, want 401", resp.StatusCode)
	}
	cleared := false
	for _, ck := range resp.Cookies() {
		if ck.Name == auth.SessionCookieName && ck.Value == "" && ck.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("stale cookie was not cleared: %v", resp.Header["Set-Cookie"])
	}
}

// TestConsoleSignInBusyReturns503 pins that the console shares the API's Argon2id
// concurrency cap (F15): with every slot busy the sign-in form answers 503 with
// Retry-After instead of starting another verification.
func TestConsoleSignInBusyReturns503(t *testing.T) {
	ts := newConsoleTestServer(t, func(o *auth.AuthOptions) { o.VerifySlots = 1 })
	release, ok := ts.authn.AcquireVerifySlot()
	if !ok {
		t.Fatal("first slot must be free")
	}
	resp := ts.post("/ui/signin", url.Values{"email": {consoleTestEmail}, "password": {consoleTestPassword}})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("saturated signin: %d, want 503", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("503 must carry Retry-After")
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == auth.SessionCookieName {
			t.Fatal("saturated signin must not set a session cookie")
		}
	}
	release()
	ts.signin()
}
