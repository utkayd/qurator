package auth

import (
	"net/http"
	"testing"
	"time"
)

// The session cookie's Secure attribute follows the operator-configured server.base_url
// scheme (F03): https → Secure, http → not Secure, unset → Secure. It is never derived
// from the request, so a proxy header cannot downgrade it.
func TestSessionCookieSecureFollowsBaseURLScheme(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		secure  bool
	}{
		{"https base url", "https://qr.example.com", true},
		{"http base url", "http://192.168.0.228:18081", false},
		{"empty base url", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, st, _ := newTestAuth(t, func(o *AuthOptions) { o.BaseURL = tc.baseURL })
			u := seedUser(t, st, "a@example.com", false)
			token, exp, err := a.IssueSession(u)
			if err != nil {
				t.Fatal(err)
			}
			if got := a.SessionCookie(token, exp).Secure; got != tc.secure {
				t.Fatalf("SessionCookie Secure=%v, want %v", got, tc.secure)
			}
			// The clearing cookie must carry the same attribute: a plain-http origin
			// cannot set a Secure cookie at all, so a Secure clear would be dropped.
			if got := a.ClearSessionCookie().Secure; got != tc.secure {
				t.Fatalf("ClearSessionCookie Secure=%v, want %v", got, tc.secure)
			}
		})
	}
}

// The other attributes stay fixed regardless of scheme.
func TestSessionCookieAttributesAreFixed(t *testing.T) {
	a, st, ck := newTestAuth(t, func(o *AuthOptions) { o.BaseURL = "http://localhost:8080" })
	u := seedUser(t, st, "a@example.com", false)
	token, exp, err := a.IssueSession(u)
	if err != nil {
		t.Fatal(err)
	}
	c := a.SessionCookie(token, exp)
	if c.Name != SessionCookieName || c.Path != "/" || c.Domain != "" || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unexpected cookie attributes: %+v", c)
	}
	if want := int(exp.Sub(ck.now()) / time.Second); c.MaxAge != want {
		t.Fatalf("MaxAge=%d, want %d", c.MaxAge, want)
	}
	clr := a.ClearSessionCookie()
	if clr.Name != SessionCookieName || clr.Path != "/" || clr.MaxAge != -1 || !clr.HttpOnly {
		t.Fatalf("unexpected clearing cookie: %+v", clr)
	}
}
