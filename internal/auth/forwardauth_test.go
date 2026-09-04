package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/utkayd/qurator/internal/config"
	"github.com/utkayd/qurator/internal/domain"
)

func fwdAuth(t *testing.T, enabled bool, cidrs ...string) *Authenticator {
	t.Helper()
	a, _, _ := newTestAuth(t, func(o *AuthOptions) {
		o.ForwardAuth = config.ForwardAuthConfig{Enabled: enabled, Header: "X-Forwarded-Email", TrustedCIDRs: cidrs}
	})
	return a
}

// probe runs the middleware and reports the identity the inner handler saw plus the status.
func probe(a *Authenticator, r *http.Request) (Identity, bool, int) {
	var id Identity
	var ok bool
	rec := httptest.NewRecorder()
	a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok = IdentityFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, r)
	return id, ok, rec.Code
}

func req(remote string, headers ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil)
	r.RemoteAddr = remote
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Add(headers[i], headers[i+1])
	}
	return r
}

func TestForwardAuthUntrustedPeerIsAnonymous(t *testing.T) {
	a := fwdAuth(t, true, "10.0.0.0/8")
	_, ok, code := probe(a, req("203.0.113.9:4444", "X-Forwarded-Email", "eve@example.com"))
	if ok || code != http.StatusOK {
		t.Fatalf("untrusted peer: ok=%v code=%d; want anonymous pass-through", ok, code)
	}
}

func TestForwardAuthTrustedPeerGetsIdentity(t *testing.T) {
	a := fwdAuth(t, true, "10.0.0.0/8")
	id, ok, code := probe(a, req("10.1.2.3:4444", "X-Forwarded-Email", "sso@example.com"))
	if !ok || code != http.StatusOK {
		t.Fatalf("trusted peer: ok=%v code=%d", ok, code)
	}
	if id.Method != MethodForwardAuth || id.IsAdmin || id.UserID == "" {
		t.Fatalf("identity = %+v", id)
	}
	u, err := a.store.GetUserByEmail(context.Background(), "sso@example.com")
	if err != nil {
		t.Fatalf("auto-provisioned user missing: %v", err)
	}
	if u.Source != domain.UserSourceForwardAuth || u.PasswordHash != "" || u.IsAdmin {
		t.Fatalf("provisioned user = %+v; want forward_auth source, no password, not admin", u)
	}
	// Second request resolves to the same user, not a duplicate.
	id2, _, _ := probe(a, req("10.1.2.3:4444", "X-Forwarded-Email", "SSO@example.com"))
	if id2.UserID != id.UserID {
		t.Fatalf("second request user %q != first %q", id2.UserID, id.UserID)
	}
}

func TestForwardAuthXFFSpoofIsIgnored(t *testing.T) {
	a := fwdAuth(t, true, "10.0.0.0/8")
	_, ok, code := probe(a, req("203.0.113.9:4444",
		"X-Forwarded-For", "10.0.0.1",
		"X-Real-IP", "10.0.0.1",
		"X-Forwarded-Email", "eve@example.com"))
	if ok || code != http.StatusOK {
		t.Fatalf("XFF spoof: ok=%v code=%d; want anonymous", ok, code)
	}
}

func TestForwardAuthDuplicateHeaderRefused(t *testing.T) {
	a := fwdAuth(t, true, "10.0.0.0/8")
	_, _, code := probe(a, req("10.0.0.7:1",
		"X-Forwarded-Email", "a@example.com",
		"X-Forwarded-Email", "b@example.com"))
	if code != http.StatusUnauthorized {
		t.Fatalf("two identity headers: code=%d want 401", code)
	}
}

func TestForwardAuthCommaJoinedRefused(t *testing.T) {
	a := fwdAuth(t, true, "10.0.0.0/8")
	_, _, code := probe(a, req("10.0.0.7:1", "X-Forwarded-Email", "a@example.com, b@example.com"))
	if code != http.StatusUnauthorized {
		t.Fatalf("comma-joined identity header: code=%d want 401", code)
	}
}

func TestForwardAuthDisabledIgnoresHeaderEntirely(t *testing.T) {
	a := fwdAuth(t, false)
	for _, r := range []*http.Request{
		req("10.0.0.7:1", "X-Forwarded-Email", "a@example.com"),
		req("127.0.0.1:1", "X-Forwarded-Email", "a@example.com", "X-Forwarded-Email", "b@example.com"),
		req("127.0.0.1:1", "X-Forwarded-Email", "a@example.com, b@example.com"),
	} {
		_, ok, code := probe(a, r)
		if ok || code != http.StatusOK {
			t.Fatalf("disabled mode: ok=%v code=%d; header must have zero effect", ok, code)
		}
	}
	if _, err := a.store.GetUserByEmail(context.Background(), "a@example.com"); err == nil {
		t.Fatal("disabled mode must not provision users")
	}
}

func TestForwardAuthConflictsWithBearer(t *testing.T) {
	a := fwdAuth(t, true, "10.0.0.0/8")
	u := seedUser(t, a.store, "owner@example.com", false)
	_, secret, _ := a.CreateToken(context.Background(), u.ID, "ci", nil)
	_, _, code := probe(a, req("10.0.0.7:1", "Authorization", "Bearer "+secret, "X-Forwarded-Email", "someone-else@example.com"))
	if code != http.StatusUnauthorized {
		t.Fatalf("bearer + forward-auth for different users: code=%d want 401", code)
	}
	id, ok, code := probe(a, req("10.0.0.7:1", "Authorization", "Bearer "+secret, "X-Forwarded-Email", "owner@example.com"))
	if !ok || code != http.StatusOK || id.Method != MethodBearer {
		t.Fatalf("agreeing assertions: ok=%v code=%d method=%q", ok, code, id.Method)
	}
}

func TestMiddlewareRejectsInvalidCredentials(t *testing.T) {
	a, st, _ := newTestAuth(t)
	u := seedUser(t, st, "a@example.com", false)
	tok, secret, _ := a.CreateToken(context.Background(), u.ID, "ci", nil)

	if _, ok, code := probe(a, req("192.0.2.1:1")); ok || code != http.StatusOK {
		t.Fatalf("anonymous must pass through: ok=%v code=%d", ok, code)
	}
	if id, ok, _ := probe(a, req("192.0.2.1:1", "Authorization", "Bearer "+secret)); !ok || id.Method != MethodBearer || id.UserID != u.ID {
		t.Fatalf("bearer identity = %+v ok=%v", id, ok)
	}
	if _, _, code := probe(a, req("192.0.2.1:1", "Authorization", "Bearer qur_garbage")); code != http.StatusUnauthorized {
		t.Fatalf("garbage bearer: code=%d", code)
	}
	if _, _, code := probe(a, req("192.0.2.1:1", "Authorization", "Basic abc")); code != http.StatusUnauthorized {
		t.Fatalf("non-bearer authorization: code=%d", code)
	}
	if _, _, code := probe(a, req("192.0.2.1:1", "Cookie", SessionCookieName+"=garbage")); code != http.StatusUnauthorized {
		t.Fatalf("garbage cookie: code=%d", code)
	}
	_ = st.RevokeToken(context.Background(), tok.ID, u.ID)
	a.tokens.reset()
	rec := httptest.NewRecorder()
	a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(rec, req("192.0.2.1:1", "Authorization", "Bearer "+secret))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"token_revoked"`) {
		t.Fatalf("revoked bearer: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRequireAuthAndAdmin(t *testing.T) {
	a, st, _ := newTestAuth(t)
	user := seedUser(t, st, "u@example.com", false)
	admin := seedUser(t, st, "adm@example.com", true)
	_, userTok, _ := a.CreateToken(context.Background(), user.ID, "u", nil)
	_, adminTok, _ := a.CreateToken(context.Background(), admin.ID, "a", nil)
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })

	run := func(h http.Handler, r *http.Request) int {
		rec := httptest.NewRecorder()
		a.Middleware(h).ServeHTTP(rec, r)
		return rec.Code
	}
	if c := run(RequireAuth(okHandler), req("192.0.2.1:1")); c != http.StatusUnauthorized {
		t.Fatalf("RequireAuth anonymous: %d", c)
	}
	if c := run(RequireAuth(okHandler), req("192.0.2.1:1", "Authorization", "Bearer "+userTok)); c != http.StatusNoContent {
		t.Fatalf("RequireAuth user: %d", c)
	}
	if c := run(RequireAdmin(okHandler), req("192.0.2.1:1")); c != http.StatusUnauthorized {
		t.Fatalf("RequireAdmin anonymous: %d", c)
	}
	if c := run(RequireAdmin(okHandler), req("192.0.2.1:1", "Authorization", "Bearer "+userTok)); c != http.StatusForbidden {
		t.Fatalf("RequireAdmin non-admin: %d", c)
	}
	if c := run(RequireAdmin(okHandler), req("192.0.2.1:1", "Authorization", "Bearer "+adminTok)); c != http.StatusNoContent {
		t.Fatalf("RequireAdmin admin: %d", c)
	}
}

func TestMethodReportsCredentialKind(t *testing.T) {
	a, st, _ := newTestAuth(t)
	u := seedUser(t, st, "a@example.com", false)
	_, secret, _ := a.CreateToken(context.Background(), u.ID, "ci", nil)
	jwtStr, exp, _ := a.IssueSession(u)

	check := func(r *http.Request, want string) {
		t.Helper()
		var got string
		rec := httptest.NewRecorder()
		a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = Method(r) })).ServeHTTP(rec, r)
		if got != want {
			t.Fatalf("Method = %q want %q (status %d)", got, want, rec.Code)
		}
	}
	check(req("192.0.2.1:1"), "")
	check(req("192.0.2.1:1", "Authorization", "Bearer "+secret), MethodBearer)
	r := req("192.0.2.1:1")
	r.AddCookie(a.SessionCookie(jwtStr, exp))
	check(r, MethodCookie)
}

func TestBootstrapOnlyWhenEmpty(t *testing.T) {
	a, st, _ := newTestAuth(t)
	ctx := context.Background()
	created, err := Bootstrap(ctx, st, "", "")
	if err != nil || created {
		t.Fatalf("unconfigured bootstrap: created=%v err=%v", created, err)
	}
	created, err = Bootstrap(ctx, st, "root@example.com", "hunter2hunter2")
	if err != nil || !created {
		t.Fatalf("first bootstrap: created=%v err=%v", created, err)
	}
	u, err := st.GetUserByEmail(ctx, "root@example.com")
	if err != nil || !u.IsAdmin || u.Source != domain.UserSourceLocal {
		t.Fatalf("bootstrap admin = %+v err=%v", u, err)
	}
	if ok, _ := VerifyPassword(u.PasswordHash, "hunter2hunter2"); !ok {
		t.Fatal("bootstrap password does not verify")
	}
	created, err = Bootstrap(ctx, st, "other@example.com", "different-password")
	if err != nil || created {
		t.Fatalf("second bootstrap must be a no-op: created=%v err=%v", created, err)
	}
	if n, _ := st.CountUsers(ctx); n != 1 {
		t.Fatalf("users = %d, want 1", n)
	}
	_ = a
}
