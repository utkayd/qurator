package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/utkayd/qurator/internal/domain"
)

// signedSession mints a session for u with explicit iat/exp, bypassing IssueSession so
// the tests can produce tokens this instance would never issue itself.
func signedSession(t *testing.T, a *Authenticator, u *domain.User, iat, exp time.Time) string {
	t.Helper()
	claims := sessionClaims{
		TokenVersion: u.TokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   u.ID,
			ID:        newID("ses_"),
			IssuedAt:  jwt.NewNumericDate(iat),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(a.signingKey)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSessionClockSkew(t *testing.T) {
	a, st, ck := newTestAuth(t)
	u := seedUser(t, st, "skew@example.com", false)
	ctx := context.Background()
	now := ck.now()
	ttl := 12 * time.Hour

	cases := []struct {
		name    string
		iat     time.Time
		exp     time.Time
		wantErr bool
	}{
		{"iat 10m in the future is rejected", now.Add(10 * time.Minute), now.Add(ttl), true},
		{"iat 30s in the future is within leeway", now.Add(30 * time.Second), now.Add(ttl), false},
		{"iat exactly at the leeway boundary is accepted", now.Add(ClockLeeway), now.Add(ttl), false},
		{"iat just past the leeway is rejected", now.Add(ClockLeeway + time.Second), now.Add(ttl), true},
		{"exp 5m in the past is rejected", now.Add(-ttl), now.Add(-5 * time.Minute), true},
		{"exp 30s in the past is within leeway", now.Add(-ttl), now.Add(-30 * time.Second), false},
		{"exp far in the past is rejected", now.Add(-2 * ttl), now.Add(-ttl), true},
		{"well-formed current token is accepted", now, now.Add(ttl), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := signedSession(t, a, u, tc.iat, tc.exp)
			got, err := a.verifySession(ctx, tok)
			if tc.wantErr {
				if !errors.Is(err, ErrUnauthorized) {
					t.Fatalf("err = %v, want ErrUnauthorized", err)
				}
				return
			}
			if err != nil || got.ID != u.ID {
				t.Fatalf("user=%v err=%v", got, err)
			}
		})
	}
}

func TestSessionWithoutExpIsRejected(t *testing.T) {
	a, st, ck := newTestAuth(t)
	u := seedUser(t, st, "noexp@example.com", false)
	claims := sessionClaims{
		TokenVersion: u.TokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:  u.ID,
			IssuedAt: jwt.NewNumericDate(ck.now()),
		},
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(a.signingKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.verifySession(context.Background(), tok); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("session without exp: err=%v, want ErrUnauthorized", err)
	}
}

func TestMiddlewareRejectsFutureDatedSessionCookie(t *testing.T) {
	a, st, ck := newTestAuth(t)
	u := seedUser(t, st, "future@example.com", false)
	now := ck.now()

	future := signedSession(t, a, u, now.Add(10*time.Minute), now.Add(12*time.Hour))
	r := req("203.0.113.9:4444")
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: future})
	if _, ok, code := probe(a, r); ok || code != http.StatusUnauthorized {
		t.Fatalf("future-dated cookie: ok=%v code=%d, want 401", ok, code)
	}

	expired := signedSession(t, a, u, now.Add(-13*time.Hour), now.Add(-time.Hour))
	r = req("203.0.113.9:4444")
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: expired})
	if _, ok, code := probe(a, r); ok || code != http.StatusUnauthorized {
		t.Fatalf("expired cookie: ok=%v code=%d, want 401", ok, code)
	}

	skewed := signedSession(t, a, u, now.Add(30*time.Second), now.Add(12*time.Hour))
	r = req("203.0.113.9:4444")
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: skewed})
	if id, ok, code := probe(a, r); !ok || code != http.StatusOK || id.UserID != u.ID {
		t.Fatalf("30s-skewed cookie: ok=%v code=%d id=%+v, want accepted", ok, code, id)
	}
}
