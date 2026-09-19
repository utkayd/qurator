package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/utkayd/qurator/internal/domain"
)

// SessionCookieName is the host-only session cookie set by POST /v1/auth/signin.
const SessionCookieName = "qurator_session"

// ClockLeeway is how far a session's iat/exp may disagree with this instance's clock
// before the session is rejected. It absorbs ordinary NTP drift between the issuing
// and verifying process (a restart, a future second instance) without letting a
// materially future-dated or expired token through.
const ClockLeeway = 60 * time.Second

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
		jwt.WithIssuedAt(), // a future iat is a forgery or a broken clock: reject, within ClockLeeway
		jwt.WithLeeway(ClockLeeway),
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

// CookieSecureForBaseURL decides the session cookie's Secure attribute from the
// operator-configured server.base_url: an https origin (or none at all) means Secure;
// an explicit http:// origin means the operator serves plain HTTP on purpose, and a
// Secure cookie would be dropped by every browser except on loopback (F03). The scheme
// comes from configuration only, never from the request's Host or forwarded headers.
func CookieSecureForBaseURL(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil || baseURL == "" {
		return true
	}
	return !strings.EqualFold(u.Scheme, "http")
}

// SessionCookie builds the cookie carrying a signed session: HttpOnly; SameSite=Strict;
// Path=/ and no Domain (host-only, instance-scoped per FR-031); Secure unless the
// configured base URL is plain http (CookieSecureForBaseURL).
func (a *Authenticator) SessionCookie(token string, exp time.Time) *http.Cookie {
	maxAge := int(exp.Sub(a.now()) / time.Second)
	if maxAge < 1 {
		maxAge = 1
	}
	return &http.Cookie{ //nolint:gosec // Secure is fixed from server.base_url at startup (CookieSecureForBaseURL), not request input
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
		Expires:  exp,
	}
}

// ClearSessionCookie returns a cookie that deletes the session cookie. It carries the
// same Secure attribute as the session cookie: a plain-http origin cannot set a Secure
// cookie at all, so a Secure clear would be silently dropped.
func (a *Authenticator) ClearSessionCookie() *http.Cookie {
	return &http.Cookie{ //nolint:gosec // Secure mirrors SessionCookie; fixed from server.base_url at startup, not request input
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	}
}
