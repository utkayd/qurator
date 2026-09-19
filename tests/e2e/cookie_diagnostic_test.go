package e2e

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/utkayd/qurator/internal/domain"
)

// cookieRejectedMessage is the diagnostic the sign-in page shows when the browser
// dropped the session cookie the server just set (F03).
const cookieRejectedMessage = "Your browser rejected the session cookie. Serve qurator over HTTPS, or set QURATOR_SERVER_BASE_URL to your http:// origin."

// A successful POST /ui/signin redirects to the console with a marker so the next
// request can tell "the browser never stored the cookie" apart from "anonymous visit".
func TestSignInRedirectCarriesMarker(t *testing.T) {
	srv, _, auth := newTestServer(t)
	auth.addUser(domain.User{ID: "usr_1", Email: "owner@example.com"}, "correct horse battery staple")

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp := doRequest(t, client, http.MethodPost, srv.URL+"/ui/signin", url.Values{
		"email":    {"owner@example.com"},
		"password": {"correct horse battery staple"},
	}, false, nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d, want 303", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if loc.Path != "/ui/" || loc.Query().Get("signed_in") != "1" {
		t.Fatalf("Location %q, want /ui/?signed_in=1", resp.Header.Get("Location"))
	}
}

// A browser that dropped the cookie arrives at the marked URL anonymous; the sign-in
// page it is bounced to must say why instead of silently showing the form again.
func TestSignInPageExplainsRejectedCookie(t *testing.T) {
	srv, _, _ := newTestServer(t)

	// No cookie jar: this client behaves like a browser that refused the cookie.
	client := &http.Client{}
	resp := doRequest(t, client, http.MethodGet, srv.URL+"/ui/?signed_in=1", nil, false, nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/ui/signin" {
		t.Fatalf("status=%d path=%s, want 200 on /ui/signin", resp.StatusCode, resp.Request.URL.Path)
	}
	doc := mustParseHTML(t, readBody(t, resp))
	alerts := findAll(doc, func(n *html.Node) bool {
		role, _ := attr(n, "role")
		return n.Type == html.ElementNode && role == "alert"
	})
	var texts []string
	for _, a := range alerts {
		texts = append(texts, strings.TrimSpace(textContent(a)))
	}
	if len(texts) != 1 || texts[0] != cookieRejectedMessage {
		t.Fatalf("alerts %q, want exactly %q", texts, cookieRejectedMessage)
	}

	// The plain anonymous visit stays quiet: no diagnostic without the marker.
	resp = doRequest(t, client, http.MethodGet, srv.URL+"/ui/", nil, false, nil)
	defer func() { _ = resp.Body.Close() }()
	if strings.Contains(readBody(t, resp), "rejected the session cookie") {
		t.Fatal("diagnostic rendered on an ordinary anonymous visit")
	}
}
