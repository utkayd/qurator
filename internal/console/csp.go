package console

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
)

// cspTemplate is the exact policy from research.md §5. It is intentionally rigid: no
// 'unsafe-inline', no 'unsafe-eval', no wildcard origins, nothing but 'self' and the
// per-request nonce. csp_test.go pins this string.
const cspTemplate = "default-src 'none'; " +
	"script-src 'self' 'nonce-%[1]s'; " +
	"style-src 'self' 'nonce-%[1]s'; " +
	"img-src 'self' data:; " +
	"font-src 'self'; " +
	"connect-src 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'none'"

type nonceCtxKey struct{}

// newNonce returns a fresh, base64url-encoded, 128-bit random nonce suitable for a CSP
// nonce-source. It contains no characters that require quoting in an HTML attribute.
func newNonce() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("console: generating nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// withCSP sets the exact CSP (with a fresh nonce), Referrer-Policy, and
// X-Content-Type-Options headers on every response, and stores the nonce in the request
// context so template rendering can stamp it onto every <script> and <style> tag.
func withCSP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce, err := newNonce()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Security-Policy", fmt.Sprintf(cspTemplate, nonce))
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		ctx := context.WithValue(r.Context(), nonceCtxKey{}, nonce)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// nonceFromContext returns the per-request CSP nonce, or "" if none was set (tests that
// call a template renderer directly without going through withCSP).
func nonceFromContext(ctx context.Context) string {
	n, _ := ctx.Value(nonceCtxKey{}).(string)
	return n
}
