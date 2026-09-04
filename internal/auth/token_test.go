package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/config"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/store"
	"github.com/utkayd/qurator/internal/store/storetest"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestAuth(t *testing.T, opts ...func(*AuthOptions)) (*Authenticator, store.Store, *clock) {
	t.Helper()
	st := storetest.NewMemStore()
	ck := &clock{t: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)}
	o := AuthOptions{SigningSecret: config.Secret("test-secret-that-is-long-enough"), SessionTTL: 12 * time.Hour}
	for _, f := range opts {
		f(&o)
	}
	a, err := New(st, o, ck.now)
	if err != nil {
		t.Fatal(err)
	}
	return a, st, ck
}

func seedUser(t *testing.T, st store.Store, email string, admin bool) *domain.User {
	t.Helper()
	u := &domain.User{ID: newID("usr_"), Email: email, IsAdmin: admin, Source: domain.UserSourceLocal}
	if err := st.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

var tokenRe = regexp.MustCompile(`^qur_[A-Za-z0-9_-]{43}$`)

func TestTokenFormatAndStoredHash(t *testing.T) {
	a, st, _ := newTestAuth(t)
	u := seedUser(t, st, "a@example.com", false)
	tok, secret, err := a.CreateToken(context.Background(), u.ID, "ci", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !tokenRe.MatchString(secret) {
		t.Fatalf("secret %q does not match qur_ + 43 base64url chars", secret)
	}
	if !regexp.MustCompile(`^tok_[0-9A-Za-z]{16}$`).MatchString(tok.ID) {
		t.Fatalf("token id %q has wrong shape", tok.ID)
	}
	raw, err := base64.RawURLEncoding.DecodeString(secret[len(TokenPrefix):])
	if err != nil || len(raw) != 32 {
		t.Fatalf("secret does not decode to 32 bytes: n=%d err=%v", len(raw), err)
	}
	stored, err := st.GetTokenByID(context.Background(), tok.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(stored.SecretHash, want[:]) != 1 {
		t.Fatal("stored hash is not SHA-256 of the presented secret")
	}
	if tok.SecretHash != nil {
		t.Fatal("returned token must not carry the hash")
	}
}

func TestTokenPepperChangesHash(t *testing.T) {
	a, st, _ := newTestAuth(t, func(o *AuthOptions) { o.TokenPepper = config.Secret("pepper") })
	u := seedUser(t, st, "a@example.com", false)
	tok, secret, err := a.CreateToken(context.Background(), u.ID, "ci", nil)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := st.GetTokenByID(context.Background(), tok.ID)
	plain := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(stored.SecretHash, plain[:]) == 1 {
		t.Fatal("with a pepper configured the stored hash must not be plain SHA-256")
	}
	if _, err := a.verifyToken(context.Background(), secret); err != nil {
		t.Fatalf("peppered token does not verify: %v", err)
	}
}

func TestTokenVerifyUsesConstantTimeCompare(t *testing.T) {
	a, st, _ := newTestAuth(t)
	u := seedUser(t, st, "a@example.com", false)
	_, secret, err := a.CreateToken(context.Background(), u.ID, "ci", nil)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	orig := constantTimeCompare
	constantTimeCompare = func(x, y []byte) int { calls.Add(1); return subtle.ConstantTimeCompare(x, y) }
	t.Cleanup(func() { constantTimeCompare = orig })

	if _, err := a.verifyToken(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("constant-time compare called %d times, want 1", calls.Load())
	}
	// A wrong secret with the same ID part must reach the compare too — never an early
	// out on a byte-wise mismatch.
	wrong := secret[:len(secret)-4] + "AAAA"
	if wrong == secret {
		wrong = secret[:len(secret)-4] + "BBBB"
	}
	if _, err := a.verifyToken(context.Background(), wrong); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong secret: err=%v want ErrUnauthorized", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("constant-time compare called %d times, want 2", calls.Load())
	}
}

func TestTokenRevokedWithinCacheTTL(t *testing.T) {
	a, st, ck := newTestAuth(t)
	u := seedUser(t, st, "a@example.com", false)
	tok, secret, err := a.CreateToken(context.Background(), u.ID, "ci", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.verifyToken(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeToken(context.Background(), tok.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	ck.advance(a.cacheTTL + time.Second)
	if _, err := a.verifyToken(context.Background(), secret); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("after revocation + one TTL: err=%v want ErrTokenRevoked", err)
	}
	// And stays revoked.
	if _, err := a.verifyToken(context.Background(), secret); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("second call: err=%v want ErrTokenRevoked", err)
	}
}

func TestTokenExpiredAndUnknown(t *testing.T) {
	a, st, ck := newTestAuth(t)
	u := seedUser(t, st, "a@example.com", false)
	exp := ck.now().Add(time.Hour)
	_, secret, err := a.CreateToken(context.Background(), u.ID, "short", &exp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.verifyToken(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	ck.advance(2 * time.Hour)
	if _, err := a.verifyToken(context.Background(), secret); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired: err=%v want ErrUnauthorized", err)
	}
	for _, bad := range []string{"", "qur_", "qur_short", "nope_" + secret[4:], secret + "x"} {
		if _, err := a.verifyToken(context.Background(), bad); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("verifyToken(%q): err=%v want ErrUnauthorized", bad, err)
		}
	}
}

func TestTokenLastUsedTouchedAtMostOncePerMinute(t *testing.T) {
	a, st, ck := newTestAuth(t)
	u := seedUser(t, st, "a@example.com", false)
	tok, secret, _ := a.CreateToken(context.Background(), u.ID, "ci", nil)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := a.verifyToken(ctx, secret); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := st.GetTokenByID(ctx, tok.ID)
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(ck.now()) {
		t.Fatalf("last_used_at = %v, want %v", got.LastUsedAt, ck.now())
	}
	first := *got.LastUsedAt
	ck.advance(30 * time.Second)
	_, _ = a.verifyToken(ctx, secret)
	got, _ = st.GetTokenByID(ctx, tok.ID)
	if !got.LastUsedAt.Equal(first) {
		t.Fatalf("touched again inside a minute: %v", got.LastUsedAt)
	}
	ck.advance(31 * time.Second)
	_, _ = a.verifyToken(ctx, secret)
	got, _ = st.GetTokenByID(ctx, tok.ID)
	if !got.LastUsedAt.After(first) {
		t.Fatalf("not touched after a minute: %v", got.LastUsedAt)
	}
}

func TestSessionTokenVersionBumpInvalidates(t *testing.T) {
	a, st, ck := newTestAuth(t)
	u := seedUser(t, st, "a@example.com", true)
	ctx := context.Background()
	jwtStr, exp, err := a.IssueSession(u)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(ck.now().Add(12 * time.Hour)) {
		t.Fatalf("exp = %v, want now+12h", exp)
	}
	got, err := a.verifySession(ctx, jwtStr)
	if err != nil || got.ID != u.ID {
		t.Fatalf("verifySession: user=%v err=%v", got, err)
	}
	if _, err := st.BumpTokenVersion(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	ck.advance(a.cacheTTL + time.Second)
	if _, err := a.verifySession(ctx, jwtStr); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("after token_version bump: err=%v want ErrUnauthorized", err)
	}
	// A fresh session for the bumped version works again.
	u2, _ := st.GetUserByID(ctx, u.ID)
	jwt2, _, _ := a.IssueSession(u2)
	if _, err := a.verifySession(ctx, jwt2); err != nil {
		t.Fatalf("fresh session rejected: %v", err)
	}
}

func TestSessionExpiryAndTamper(t *testing.T) {
	a, st, ck := newTestAuth(t)
	u := seedUser(t, st, "a@example.com", false)
	ctx := context.Background()
	jwtStr, _, _ := a.IssueSession(u)
	ck.advance(13 * time.Hour)
	if _, err := a.verifySession(ctx, jwtStr); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired session: err=%v", err)
	}
	other, _ := New(st, AuthOptions{SigningSecret: "another-secret"}, ck.now)
	jwt2, _, _ := other.IssueSession(u)
	if _, err := a.verifySession(ctx, jwt2); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("foreign signature accepted: err=%v", err)
	}
	if _, err := a.verifySession(ctx, "not.a.jwt"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("garbage accepted: err=%v", err)
	}
}

func TestNewRefusesMissingSecretOutsideDevMode(t *testing.T) {
	st := storetest.NewMemStore()
	if _, err := New(st, AuthOptions{}, time.Now); err == nil {
		t.Fatal("expected error with no signing secret and dev_mode off")
	}
	a, err := New(st, AuthOptions{DevMode: true}, time.Now)
	if err != nil || a == nil {
		t.Fatalf("dev mode should derive an ephemeral key: %v", err)
	}
}
