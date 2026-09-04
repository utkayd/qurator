package auth

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/utkayd/qurator/internal/domain"
)

// SessionCookieName is the host-only session cookie set by POST /v1/auth/signin.
const SessionCookieName = "qurator_session"

// sessionClaims is the HS256 payload: sub (user ID), jti, tv (token_version), iat, exp.
type sessionClaims struct {
	TokenVersion int64 `json:"tv"`
	jwt.RegisteredClaims
}

// IssueSession signs a session for u that expires after the configured TTL.
func (a *Authenticator) IssueSession(u *domain.User) (token string, exp time.Time, err error) {
	now := a.now().UTC()
	exp = now.Add(a.sessionTTL)
	claims := sessionClaims{
		TokenVersion: u.TokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   u.ID,
			ID:        newID("ses_"),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
	token, err = jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(a.signingKey)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: sign session: %w", err)
	}
	return token, exp, nil
}

// verifySession parses and validates a session JWT and checks its tv claim against the
// user's current token_version (through the positive cache, so a bump propagates within
// one cache TTL).
func (a *Authenticator) verifySession(ctx context.Context, token string) (*domain.User, error) {
	var claims sessionClaims
	_, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		return a.signingKey, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithTimeFunc(a.now),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)
	if err != nil || claims.Subject == "" {
		return nil, ErrUnauthorized
	}
	u, err := a.userByID(ctx, claims.Subject)
	if err != nil {
		return nil, err
	}
	if u.TokenVersion != claims.TokenVersion {
		return nil, ErrUnauthorized
	}
	return u, nil
}

// SessionCookie builds the cookie carrying a signed session: HttpOnly; Secure;
// SameSite=Strict; Path=/ and no Domain (host-only, instance-scoped per FR-031).
func (a *Authenticator) SessionCookie(token string, exp time.Time) *http.Cookie {
	maxAge := int(exp.Sub(a.now()) / time.Second)
	if maxAge < 1 {
		maxAge = 1
	}
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
		Expires:  exp,
	}
}

// ClearSessionCookie returns a cookie that deletes the session cookie.
func ClearSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	}
}
