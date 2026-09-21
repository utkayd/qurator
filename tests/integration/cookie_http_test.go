package integration

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
)

// The session cookie's Secure attribute follows QURATOR_SERVER_BASE_URL's scheme
// (F03). A browser on a plain-http LAN address (not loopback, which browsers and Go's
// cookie jar both treat as a secure context) drops a Secure cookie, which is exactly
// how sign-in used to bounce straight back to the sign-in page.

// itLANJar is a cookie jar that behaves like a browser reached over plain http from
// another machine: Secure cookies are never stored.
type itLANJar struct{ http.CookieJar }

func (j itLANJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	kept := cookies[:0]
	for _, c := range cookies {
		if !c.Secure {
			kept = append(kept, c)
		}
	}
	j.CookieJar.SetCookies(u, kept)
}

// itSetCookieAttrs signs in through the API and returns the raw Set-Cookie line for
// the session cookie.
func itSetCookieAttrs(t *testing.T, base string) string {
	t.Helper()
	_, r := itSignin(t, base, itAdminEmail, itAdminPassword)
	for _, line := range r.Header.Values("Set-Cookie") {
		if strings.HasPrefix(line, itSessionCookie+"=") {
			return line
		}
	}
	t.Fatalf("no %s Set-Cookie: %v", itSessionCookie, r.Header)
	return ""
}

func itHasSecure(line string) bool {
	for _, part := range strings.Split(line, ";") {
		if strings.EqualFold(strings.TrimSpace(part), "Secure") {
			return true
		}
	}
	return false
}

func TestSessionCookie_SecureFollowsBaseURLScheme(t *testing.T) {
	t.Run("http base url is not Secure", func(t *testing.T) {
		p := itStartWithBase(t, t.TempDir(), itDevAdminEnv())
		line := itSetCookieAttrs(t, p.Base)
		if itHasSecure(line) {
			t.Fatalf("cookie is Secure with an http base URL: %s", line)
		}
		if !strings.Contains(line, "HttpOnly") || !strings.Contains(line, "SameSite=Strict") {
			t.Fatalf("HttpOnly/SameSite=Strict must stay: %s", line)
		}
	})
	t.Run("https base url is Secure", func(t *testing.T) {
		env := itDevAdminEnv()
		env["QURATOR_SERVER_BASE_URL"] = "https://qr.example.com"
		p := itStart(t, t.TempDir(), env)
		if line := itSetCookieAttrs(t, p.Base); !itHasSecure(line) {
			t.Fatalf("cookie lacks Secure with an https base URL: %s", line)
		}
	})
	t.Run("empty base url is Secure", func(t *testing.T) {
		p := itStart(t, t.TempDir(), itDevAdminEnv())
		if line := itSetCookieAttrs(t, p.Base); !itHasSecure(line) {
			t.Fatalf("cookie lacks Secure with no base URL: %s", line)
		}
	})
}

// itConsoleSignIn drives the HTML sign-in form with a LAN-browser cookie jar and follows
// redirects, returning where the browser-like client finally landed and the page.
func itConsoleSignIn(t *testing.T, base string) (landed string, page string) {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: itLANJar{jar}}
	form := url.Values{"email": {itAdminEmail}, "password": {itAdminPassword}}
	resp, err := client.Post(base+"/ui/signin", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("final status %d on %s", resp.StatusCode, resp.Request.URL)
	}
	return resp.Request.URL.Path, string(body)
}

const itCookieRejected = "Your browser rejected the session cookie."

func TestConsoleSignIn_PlainHTTP(t *testing.T) {
	t.Run("lands on the console with an http base url", func(t *testing.T) {
		p := itStartWithBase(t, t.TempDir(), itDevAdminEnv())
		landed, page := itConsoleSignIn(t, p.Base)
		if landed != "/ui/" {
			t.Fatalf("landed on %s, want /ui/", landed)
		}
		if strings.Contains(page, itCookieRejected) {
			t.Fatal("diagnostic shown although the cookie was accepted")
		}
	})
	t.Run("explains the rejected cookie with an https base url", func(t *testing.T) {
		env := itDevAdminEnv()
		env["QURATOR_SERVER_BASE_URL"] = "https://qr.example.com"
		p := itStart(t, t.TempDir(), env)
		landed, page := itConsoleSignIn(t, p.Base)
		if landed != "/ui/signin" {
			t.Fatalf("landed on %s, want /ui/signin", landed)
		}
		if !strings.Contains(page, itCookieRejected) {
			t.Fatalf("sign-in page shows no cookie diagnostic:\n%s", page)
		}
	})
}
