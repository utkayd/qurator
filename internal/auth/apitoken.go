package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/store"
)

// TokenPrefix identifies an API token on the wire.
const TokenPrefix = "qur_"

// tokenBytes is the random material behind a token; the first idBytes of it are the
// token ID material, the rest is the secret proper. See the package doc for the format.
const tokenBytes = 32

// tokenEncodedLen is len(base64url(32 bytes)) without padding.
const tokenEncodedLen = 43

// constantTimeCompare is a variable so token_test.go can wrap it with a counting spy and
// prove verification never takes a byte-wise early exit.
var constantTimeCompare = subtle.ConstantTimeCompare

// hashToken is SHA-256 of the whole wire string, or HMAC-SHA256 under the pepper so a
// stolen database alone does not permit offline verification.
func (a *Authenticator) hashToken(secret string) []byte {
	if len(a.pepper) > 0 {
		m := hmac.New(sha256.New, a.pepper)
		m.Write([]byte(secret))
		return m.Sum(nil)
	}
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

// parseToken derives the token ID from a presented secret. ok is false for anything that
// is not exactly "qur_" + 43 base64url characters decoding to 32 bytes.
func parseToken(secret string) (id string, ok bool) {
	if !strings.HasPrefix(secret, TokenPrefix) || len(secret) != len(TokenPrefix)+tokenEncodedLen {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(secret[len(TokenPrefix):])
	if err != nil || len(raw) != tokenBytes {
		return "", false
	}
	return encodeID("tok_", raw), true
}

// CreateToken mints a named token for userID and persists only its hash. The returned
// secret is the one and only time it exists in the clear (FR-033/FR-034). The returned
// record carries no hash.
func (a *Authenticator) CreateToken(ctx context.Context, userID, name string, expiresAt *time.Time) (*domain.APIToken, string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", fmt.Errorf("auth: token material: %w", err)
	}
	secret := TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	now := a.now().UTC()
	t := &domain.APIToken{
		ID:         encodeID("tok_", raw),
		UserID:     userID,
		Name:       name,
		SecretHash: a.hashToken(secret),
		CreatedAt:  now,
	}
	if expiresAt != nil {
		e := expiresAt.UTC()
		t.ExpiresAt = &e
	}
	if err := a.store.CreateToken(ctx, t); err != nil {
		return nil, "", fmt.Errorf("auth: create token: %w", err)
	}
	out := *t
	out.SecretHash = nil
	return &out, secret, nil
}

// verifyToken resolves a presented secret to its live token record, consulting the
// positive cache first. Revoked → ErrTokenRevoked; everything else that fails →
// ErrUnauthorized. On success last_used_at is touched at most once per minute.
func (a *Authenticator) verifyToken(ctx context.Context, secret string) (*domain.APIToken, error) {
	id, ok := parseToken(secret)
	if !ok {
		return nil, ErrUnauthorized
	}
	t, cached := a.tokens.get(id)
	if !cached {
		var err error
		t, err = a.store.GetTokenByID(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, ErrUnauthorized
			}
			return nil, fmt.Errorf("auth: token lookup: %w", err)
		}
	}
	// The compare always runs, even for a revoked or expired record, so the response
	// time does not depend on which check failed.
	match := constantTimeCompare(a.hashToken(secret), t.SecretHash) == 1
	if !match {
		return nil, ErrUnauthorized
	}
	if t.Revoked() {
		return nil, ErrTokenRevoked
	}
	now := a.now()
	if t.ExpiresAt != nil && !now.Before(*t.ExpiresAt) {
		return nil, ErrUnauthorized
	}
	if !cached {
		a.tokens.put(id, t)
	}
	a.touch(ctx, id, now)
	out := *t
	out.SecretHash = nil
	return &out, nil
}

// touch persists last_used_at at most once per touchInterval per token.
func (a *Authenticator) touch(ctx context.Context, id string, now time.Time) {
	a.touchMu.Lock()
	last, seen := a.lastTouch[id]
	if seen && now.Sub(last) < touchInterval {
		a.touchMu.Unlock()
		return
	}
	a.lastTouch[id] = now
	if len(a.lastTouch) > 8192 {
		for k, v := range a.lastTouch {
			if now.Sub(v) >= touchInterval {
				delete(a.lastTouch, k)
			}
		}
	}
	a.touchMu.Unlock()
	if err := a.store.TouchTokenLastUsed(ctx, id, now); err != nil {
		a.log.DebugContext(ctx, "auth: touch token last_used_at", "token_id", id, "err", err)
	}
}

// userByID resolves a user through the positive cache.
func (a *Authenticator) userByID(ctx context.Context, id string) (*domain.User, error) {
	if u, ok := a.users.get(id); ok {
		return u, nil
	}
	u, err := a.store.GetUserByID(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrUnauthorized
		}
		return nil, fmt.Errorf("auth: user lookup: %w", err)
	}
	a.users.put(id, u)
	return u, nil
}
