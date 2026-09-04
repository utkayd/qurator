// Package contract holds black-box tests that pin the HTTP surface described in
// specs/001-qr-service-baseline/contracts/openapi.yaml.
package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/auth"
	"github.com/utkayd/qurator/internal/config"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi"
	"github.com/utkayd/qurator/internal/httpapi/middleware"
	v1 "github.com/utkayd/qurator/internal/httpapi/v1"
	"github.com/utkayd/qurator/internal/store"
	"github.com/utkayd/qurator/internal/store/storetest"
)

const (
	adminEmail = "admin@example.com"
	userEmail  = "user@example.com"
	password   = "correct horse battery staple"
)

type env struct {
	t     *testing.T
	h     http.Handler
	st    store.Store
	a     *auth.Authenticator
	admin *domain.User
	user  *domain.User
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := storetest.NewMemStore()
	a, err := auth.New(st, auth.AuthOptions{
		SigningSecret: config.Secret("contract-test-signing-secret"),
		SessionTTL:    12 * time.Hour,
	}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if created, err := auth.Bootstrap(ctx, st, adminEmail, password); err != nil || !created {
		t.Fatalf("bootstrap: created=%v err=%v", created, err)
	}
	admin, _ := st.GetUserByEmail(ctx, adminEmail)
	user := &domain.User{ID: "usr_contractuser0001", Email: userEmail, PasswordHash: admin.PasswordHash, Source: domain.UserSourceLocal}
	if err := st.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	h := httpapi.NewRouter(httpapi.Handlers{
		Auth:   v1.NewAuthHandler(a, st),
		Tokens: v1.NewTokensHandler(a, st),
		Admin:  v1.NewAdminHandler(st),
	}, httpapi.Options{
		Auth: a.Middleware,
		CSRF: middleware.CSRF(auth.Method),
	})
	return &env{t: t, h: h, st: st, a: a, admin: admin, user: user}
}

func (e *env) do(method, path string, body any, mod ...func(*http.Request)) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rd)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for _, m := range mod {
		m(r)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func (e *env) signin(email string) *http.Cookie {
	e.t.Helper()
	rec := e.do(http.MethodPost, "/v1/auth/signin", map[string]string{"email": email, "password": password})
	if rec.Code != http.StatusOK {
		e.t.Fatalf("signin: %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			return c
		}
	}
	e.t.Fatal("signin set no session cookie")
	return nil
}

func withCookie(c *http.Cookie) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value}) }
}

func withBearer(secret string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+secret) }
}

func withCSRF(r *http.Request) { r.Header.Set(middleware.CSRFHeader, "1") }

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e httpapi.ErrorBody
	decode(t, rec, &e)
	return string(e.Error.Code)
}

func (e *env) bearer(u *domain.User) string {
	e.t.Helper()
	_, secret, err := e.a.CreateToken(context.Background(), u.ID, "test", nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return secret
}

func TestSigninCookieAttributes(t *testing.T) {
	e := newEnv(t)
	rec := e.do(http.MethodPost, "/v1/auth/signin", map[string]string{"email": adminEmail, "password": password})
	if rec.Code != http.StatusOK {
		t.Fatalf("signin: %d %s", rec.Code, rec.Body.String())
	}
	var u map[string]any
	decode(t, rec, &u)
	if u["email"] != adminEmail || u["is_admin"] != true || u["source"] != "local" || u["id"] == nil || u["created_at"] == nil {
		t.Fatalf("user body = %v", u)
	}
	if _, leaked := u["password_hash"]; leaked {
		t.Fatal("password hash leaked in user body")
	}
	raw := rec.Header().Get("Set-Cookie")
	if raw == "" {
		t.Fatal("no Set-Cookie header")
	}
	var c *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == auth.SessionCookieName {
			c = ck
		}
	}
	if c == nil {
		t.Fatalf("no %s cookie in %q", auth.SessionCookieName, raw)
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" {
		t.Fatalf("cookie attributes wrong: %q", raw)
	}
	if strings.Contains(strings.ToLower(raw), "domain=") {
		t.Fatalf("cookie must not carry Domain: %q", raw)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("signin response must be no-store, got %q", cc)
	}
}

func TestSigninUniform401(t *testing.T) {
	e := newEnv(t)
	for _, body := range []map[string]string{
		{"email": adminEmail, "password": "wrong"},
		{"email": "nobody@example.com", "password": password},
	} {
		rec := e.do(http.MethodPost, "/v1/auth/signin", body)
		if rec.Code != http.StatusUnauthorized || errCode(t, rec) != "unauthorized" {
			t.Fatalf("signin %v: %d %s", body, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Set-Cookie") != "" {
			t.Fatal("failed signin must not set a cookie")
		}
	}
	rec := e.do(http.MethodPost, "/v1/auth/signin", map[string]string{"email": adminEmail})
	if rec.Code != http.StatusBadRequest || errCode(t, rec) != "invalid_request" {
		t.Fatalf("missing password: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(http.MethodPost, "/v1/auth/signin", map[string]any{"email": adminEmail, "password": password, "extra": 1})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMeWithCookieAndBearer(t *testing.T) {
	e := newEnv(t)
	c := e.signin(adminEmail)
	rec := e.do(http.MethodGet, "/v1/auth/me", nil, withCookie(c))
	var u map[string]any
	decode(t, rec, &u)
	if rec.Code != http.StatusOK || u["email"] != adminEmail {
		t.Fatalf("me via cookie: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(http.MethodGet, "/v1/auth/me", nil, withBearer(e.bearer(e.user)))
	decode(t, rec, &u)
	if rec.Code != http.StatusOK || u["email"] != userEmail || u["is_admin"] != false {
		t.Fatalf("me via bearer: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(http.MethodGet, "/v1/auth/me", nil)
	if rec.Code != http.StatusUnauthorized || errCode(t, rec) != "unauthorized" {
		t.Fatalf("me anonymous: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCSRFAppliesToCookieOnly(t *testing.T) {
	e := newEnv(t)
	c := e.signin(userEmail)
	rec := e.do(http.MethodPost, "/v1/tokens", map[string]string{"name": "ci"}, withCookie(c))
	if rec.Code != http.StatusForbidden || errCode(t, rec) != "forbidden" {
		t.Fatalf("cookie without CSRF header: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(http.MethodPost, "/v1/tokens", map[string]string{"name": "ci"}, withCookie(c), withCSRF)
	if rec.Code != http.StatusCreated {
		t.Fatalf("cookie with CSRF header: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(http.MethodPost, "/v1/tokens", map[string]string{"name": "ci2"}, withBearer(e.bearer(e.user)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("bearer without CSRF header: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTokensSecretShownOnce(t *testing.T) {
	e := newEnv(t)
	bearer := e.bearer(e.user)
	exp := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	rec := e.do(http.MethodPost, "/v1/tokens", map[string]any{"name": "ci-pipeline", "expires_at": exp.Format(time.RFC3339)}, withBearer(bearer))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	decode(t, rec, &created)
	secret, _ := created["secret"].(string)
	if !strings.HasPrefix(secret, "qur_") || len(secret) != 4+43 {
		t.Fatalf("secret = %q", secret)
	}
	if created["name"] != "ci-pipeline" || created["id"] == nil || created["created_at"] == nil || created["expires_at"] == nil {
		t.Fatalf("created body = %v", created)
	}
	if created["revoked_at"] != nil || created["last_used_at"] != nil {
		t.Fatalf("fresh token must have null revoked_at/last_used_at: %v", created)
	}

	// The new secret authenticates.
	if r := e.do(http.MethodGet, "/v1/auth/me", nil, withBearer(secret)); r.Code != http.StatusOK {
		t.Fatalf("new token rejected: %d %s", r.Code, r.Body.String())
	}

	rec = e.do(http.MethodGet, "/v1/tokens", nil, withBearer(bearer))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), secret[4:]) {
		t.Fatalf("list leaks secret material: %s", rec.Body.String())
	}
	var list []map[string]any
	decode(t, rec, &list)
	if len(list) != 2 {
		t.Fatalf("list = %v, want 2 tokens (helper + created)", list)
	}
	found := false
	for _, it := range list {
		if it["id"] == created["id"] {
			found = true
		}
		for k := range it {
			switch k {
			case "id", "name", "created_at", "last_used_at", "revoked_at", "expires_at":
			default:
				t.Fatalf("unexpected field %q in ApiTokenSummary", k)
			}
		}
	}
	if !found {
		t.Fatal("created token missing from list")
	}

	// Revoke: idempotent 204; the token stops working; unknown id is 404.
	id := created["id"].(string)
	if r := e.do(http.MethodDelete, "/v1/tokens/"+id, nil, withBearer(bearer)); r.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", r.Code, r.Body.String())
	}
	if r := e.do(http.MethodDelete, "/v1/tokens/"+id, nil, withBearer(bearer)); r.Code != http.StatusNoContent {
		t.Fatalf("second revoke: %d %s", r.Code, r.Body.String())
	}
	if r := e.do(http.MethodDelete, "/v1/tokens/tok_doesnotexist0000", nil, withBearer(bearer)); r.Code != http.StatusNotFound {
		t.Fatalf("revoke unknown: %d %s", r.Code, r.Body.String())
	}
	// Another user cannot revoke it (and learns nothing).
	if r := e.do(http.MethodDelete, "/v1/tokens/"+id, nil, withBearer(e.bearer(e.admin))); r.Code != http.StatusNotFound {
		t.Fatalf("revoke as other user: %d %s", r.Code, r.Body.String())
	}

	rec = e.do(http.MethodPost, "/v1/tokens", map[string]any{"name": ""}, withBearer(bearer))
	if rec.Code != http.StatusBadRequest || errCode(t, rec) != "invalid_request" {
		t.Fatalf("empty name: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(http.MethodPost, "/v1/tokens", map[string]any{"name": "past", "expires_at": "2000-01-01T00:00:00Z"}, withBearer(bearer))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("past expiry: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRevokedTokenRefusedAfterCacheTTL(t *testing.T) {
	// The contract env uses the real clock; the 30s cache is exercised with a fake
	// clock in internal/auth. Here we pin the wire-level code with a fresh Authenticator
	// that has never cached the token.
	e := newEnv(t)
	tok, secret, err := e.a.CreateToken(context.Background(), e.user.ID, "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.RevokeToken(context.Background(), tok.ID, e.user.ID); err != nil {
		t.Fatal(err)
	}
	rec := e.do(http.MethodGet, "/v1/auth/me", nil, withBearer(secret))
	if rec.Code != http.StatusUnauthorized || errCode(t, rec) != "token_revoked" {
		t.Fatalf("revoked bearer: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSignout(t *testing.T) {
	e := newEnv(t)
	c := e.signin(userEmail)
	rec := e.do(http.MethodPost, "/v1/auth/signout", nil, withCookie(c))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("signout without CSRF header: %d", rec.Code)
	}
	rec = e.do(http.MethodPost, "/v1/auth/signout", nil, withCookie(c), withCSRF)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("signout: %d %s", rec.Code, rec.Body.String())
	}
	var cleared *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == auth.SessionCookieName {
			cleared = ck
		}
	}
	if cleared == nil || cleared.Value != "" || cleared.MaxAge >= 0 {
		t.Fatalf("signout must clear the cookie: %v", rec.Header().Get("Set-Cookie"))
	}
	if rec := e.do(http.MethodPost, "/v1/auth/signout", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous signout: %d", rec.Code)
	}
}

func TestAdminReleaseAlias(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	code := &domain.Code{ID: "cod_aliasownercode01", ShortCode: "spring-sale", IsAlias: true, UserID: e.user.ID, Destination: "https://example.com", State: domain.CodeActive}
	if err := e.st.CreateCode(ctx, code); err != nil {
		t.Fatal(err)
	}
	userBearer, adminBearer := e.bearer(e.user), e.bearer(e.admin)

	rec := e.do(http.MethodDelete, "/v1/admin/aliases/spring-sale", nil, withBearer(userBearer))
	if rec.Code != http.StatusForbidden || errCode(t, rec) != "forbidden" {
		t.Fatalf("non-admin: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(http.MethodDelete, "/v1/admin/aliases/spring-sale", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(http.MethodDelete, "/v1/admin/aliases/spring-sale", nil, withBearer(adminBearer))
	if rec.Code != http.StatusConflict || errCode(t, rec) != "conflict" {
		t.Fatalf("live code: %d %s", rec.Code, rec.Body.String())
	}
	if err := e.st.DeleteCode(ctx, code.ID, e.user.ID); err != nil {
		t.Fatal(err)
	}
	rec = e.do(http.MethodDelete, "/v1/admin/aliases/spring-sale", nil, withBearer(adminBearer))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("release: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(http.MethodDelete, "/v1/admin/aliases/spring-sale", nil, withBearer(adminBearer))
	if rec.Code != http.StatusNotFound || errCode(t, rec) != "not_found" {
		t.Fatalf("already released: %d %s", rec.Code, rec.Body.String())
	}
	if ok, _ := e.st.IsAliasAvailable(ctx, "spring-sale"); !ok {
		t.Fatal("alias should be available after release")
	}
}
