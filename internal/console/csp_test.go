package console

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/utkayd/qurator/internal/domain"
)

const wantCSP = "default-src 'none'; " +
	"script-src 'self' 'nonce-%[1]s'; " +
	"style-src 'self' 'nonce-%[1]s'; " +
	"img-src 'self' data:; " +
	"font-src 'self'; " +
	"connect-src 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'none'"

func newSignedInRequest(t *testing.T, auth *fakeAuth, method, target string) *http.Request {
	t.Helper()
	rr := httptest.NewRecorder()
	if _, err := auth.SignIn(t.Context(), rr, "admin@example.com", "hunter2"); err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	req := httptest.NewRequest(method, target, nil)
	for _, c := range rr.Result().Cookies() {
		req.AddCookie(c)
	}
	return req
}

func TestCSPHeaderExactAndFreshPerRequest(t *testing.T) {
	deps, auth, _, _ := newTestDeps()
	auth.addUser(domain.User{ID: "usr_1", Email: "admin@example.com"}, "hunter2")
	h := New(deps)

	var nonces []string
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/ui/signin", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)

		csp := rr.Header().Get("Content-Security-Policy")
		if csp == "" {
			t.Fatalf("missing Content-Security-Policy header")
		}
		nonce := extractNonce(t, csp)
		if got := formatCSP(nonce); got != csp {
			t.Fatalf("CSP mismatch:\n got:  %s\n want: %s", csp, got)
		}
		if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
			t.Fatalf("CSP must not contain unsafe directives: %s", csp)
		}
		if rr.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("Referrer-Policy = %q, want no-referrer", rr.Header().Get("Referrer-Policy"))
		}
		if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("X-Content-Type-Options = %q, want nosniff", rr.Header().Get("X-Content-Type-Options"))
		}
		nonces = append(nonces, nonce)
	}
	if nonces[0] == nonces[1] || nonces[1] == nonces[2] {
		t.Fatalf("nonce must be fresh per request, got %v", nonces)
	}
}

func extractNonce(t *testing.T, csp string) string {
	t.Helper()
	re := regexp.MustCompile(`'nonce-([A-Za-z0-9_-]+)'`)
	m := re.FindStringSubmatch(csp)
	if m == nil {
		t.Fatalf("no nonce found in CSP: %s", csp)
	}
	return m[1]
}

func formatCSP(nonce string) string {
	return strings.ReplaceAll(strings.ReplaceAll(wantCSP, "%[1]s", nonce), "%[1]s", nonce)
}

func TestEveryScriptAndStyleTagCarriesTheResponseNonce(t *testing.T) {
	deps, _, _, _ := newTestDeps()
	h := New(deps)
	reqs := pagesToCheckHandler(t, h)

	for name, req := range reqs {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code >= 400 {
			t.Fatalf("%s: unexpected status %d: %s", name, rr.Code, rr.Body.String())
		}
		csp := rr.Header().Get("Content-Security-Policy")
		nonce := extractNonce(t, csp)

		checkScriptAndStyleNonces(t, name, rr.Body.String(), nonce)
	}
}

// pagesToCheckHandler is like pagesToCheck but seeds through the handler's own deps so
// the returned requests carry cookies valid for h.
func pagesToCheckHandler(t *testing.T, h *Handler) map[string]*http.Request {
	t.Helper()
	auth := h.deps.Auth.(*fakeAuth)
	codes := h.deps.Codes.(*fakeCodes)
	tokens := h.deps.Tokens.(*fakeTokens)
	auth.addUser(domain.User{ID: "usr_1", Email: "admin@example.com"}, "hunter2")
	code, err := codes.Create(t.Context(), "usr_1", CreateCodeInput{Destination: "https://example.com"})
	if err != nil {
		t.Fatalf("seed code: %v", err)
	}
	if _, _, err := tokens.Create(t.Context(), "usr_1", "seed", nil); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	return map[string]*http.Request{
		"signin":      httptest.NewRequest(http.MethodGet, "/ui/signin", nil),
		"codes_list":  newSignedInRequest(t, auth, http.MethodGet, "/ui/"),
		"code_new":    newSignedInRequest(t, auth, http.MethodGet, "/ui/codes/new"),
		"code_detail": newSignedInRequest(t, auth, http.MethodGet, "/ui/codes/"+code.ID),
		"tokens":      newSignedInRequest(t, auth, http.MethodGet, "/ui/tokens"),
	}
}

func checkScriptAndStyleNonces(t *testing.T, page, body, nonce string) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("%s: parsing HTML: %v", page, err)
	}
	found := 0
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			found++
			var got string
			var hasNonce bool
			for _, a := range n.Attr {
				if a.Key == "nonce" {
					got = a.Val
					hasNonce = true
				}
				if a.Key == "on" || strings.HasPrefix(a.Key, "hx-on") {
					t.Fatalf("%s: found forbidden hx-on attribute on <%s>", page, n.Data)
				}
			}
			if !hasNonce {
				t.Fatalf("%s: <%s> tag missing nonce attribute", page, n.Data)
			}
			if got != nonce {
				t.Fatalf("%s: <%s> nonce = %q, want response nonce %q", page, n.Data, got, nonce)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if found == 0 {
		t.Fatalf("%s: expected at least one <script> or <style> tag", page)
	}
}

func TestNoHxOnAttributeAnywhereInEmbeddedTemplates(t *testing.T) {
	err := walkEmbedded(func(path string, data []byte) error {
		if !strings.HasSuffix(path, ".html") {
			return nil
		}
		if strings.Contains(string(data), "hx-on") {
			t.Errorf("%s: contains a forbidden hx-on attribute", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking embedded templates: %v", err)
	}
}

// TestSigninFormDoesNotLeakUnsafeCSP is a smoke test that a real (net/http) round trip
// through the handler, not just direct template rendering, produces the header.
func TestSigninFormDoesNotLeakUnsafeCSP(t *testing.T) {
	deps, _, _, _ := newTestDeps()
	h := New(deps)
	req := httptest.NewRequest(http.MethodGet, "/ui/signin", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	body, err := io.ReadAll(rr.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "action=\"/ui/signin\"") {
		t.Fatalf("sign-in form not found in rendered page")
	}
}
