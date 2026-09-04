package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi"
	"github.com/utkayd/qurator/internal/store"
)

// Authentication methods reported in Identity.Method (and by Method for the CSRF layer).
const (
	MethodBearer      = "bearer"
	MethodCookie      = "cookie"
	MethodForwardAuth = "forward_auth"
)

// Identity is the verified caller attached to the request context.
type Identity struct {
	UserID  string
	IsAdmin bool
	Method  string // MethodBearer, MethodCookie, or MethodForwardAuth
}

type ctxKey struct{}

// IdentityFrom returns the verified identity, if any.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}

// IsAuthenticated reports whether r carries a verified identity.
func IsAuthenticated(r *http.Request) bool {
	_, ok := IdentityFrom(r.Context())
	return ok
}

// Method reports how r was authenticated ("" for anonymous). It satisfies
// middleware.AuthMethodFunc; the CSRF layer must run inside this middleware.
func Method(r *http.Request) string {
	id, _ := IdentityFrom(r.Context())
	return id.Method
}

// RequireAuth rejects anonymous requests with 401.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsAuthenticated(r) {
			httpapi.WriteError(w, httpapi.CodeUnauthorized, "Authentication is required.", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin rejects anonymous requests with 401 and non-admins with 403.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := IdentityFrom(r.Context())
		if !ok {
			httpapi.WriteError(w, httpapi.CodeUnauthorized, "Authentication is required.", nil)
			return
		}
		if !id.IsAdmin {
			httpapi.WriteError(w, httpapi.CodeForbidden, "This operation requires an administrator.", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Middleware is the ONE credential-verification path (FR-031). It implements
// httpapi.Middleware. Order: Authorization: Bearer → session cookie → forward-auth
// header (only when enabled and the TCP peer is inside a trusted CIDR).
//
// Anonymous requests pass through — handlers decide with RequireAuth/RequireAdmin. A
// credential that is present but invalid is refused with 401 (unauthorized or
// token_revoked), and so are conflicting identity assertions (FR-038): a duplicated or
// comma-joined identity header, or two credentials resolving to different users.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok, err := a.resolve(r)
		if err != nil {
			a.reject(w, r, err)
			return
		}
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

// errConflict marks two assertions that disagree.
var errConflict = errors.New("auth: conflicting identity assertions")

func (a *Authenticator) reject(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrTokenRevoked):
		httpapi.WriteError(w, httpapi.CodeTokenRevoked, "The presented API token has been revoked.", nil)
	case errors.Is(err, errConflict):
		httpapi.WriteError(w, httpapi.CodeUnauthorized, "The request carries conflicting identity assertions.", nil)
	case errors.Is(err, ErrUnauthorized):
		if _, hasCookie := r.Cookie(SessionCookieName); hasCookie == nil {
			// A stale browser session: clear it so the next request is anonymous.
			http.SetCookie(w, ClearSessionCookie())
		}
		httpapi.WriteError(w, httpapi.CodeUnauthorized, "The presented credential is not valid.", nil)
	default:
		httpapi.Internal(w, r, err)
	}
}

// resolve evaluates every eligible credential on r. The first one present decides
// Method; all present ones must be valid and agree on the user.
func (a *Authenticator) resolve(r *http.Request) (Identity, bool, error) {
	ctx := r.Context()
	var (
		id    Identity
		found bool
	)
	accept := func(u *domain.User, method string) error {
		if found {
			if u.ID != id.UserID {
				return errConflict
			}
			return nil
		}
		id = Identity{UserID: u.ID, IsAdmin: u.IsAdmin, Method: method}
		found = true
		return nil
	}

	if authz := r.Header.Get("Authorization"); authz != "" {
		scheme, secret, _ := strings.Cut(authz, " ")
		if !strings.EqualFold(scheme, "Bearer") {
			return Identity{}, false, ErrUnauthorized
		}
		t, err := a.verifyToken(ctx, strings.TrimSpace(secret))
		if err != nil {
			return Identity{}, false, err
		}
		u, err := a.userByID(ctx, t.UserID)
		if err != nil {
			return Identity{}, false, err
		}
		if err := accept(u, MethodBearer); err != nil {
			return Identity{}, false, err
		}
	}

	if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
		u, err := a.verifySession(ctx, c.Value)
		if err != nil {
			return Identity{}, false, err
		}
		if err := accept(u, MethodCookie); err != nil {
			return Identity{}, false, err
		}
	}

	if a.fwdEnabled && a.peerTrusted(r.RemoteAddr) {
		vals := r.Header.Values(a.fwdHeader)
		if len(vals) > 1 {
			return Identity{}, false, errConflict
		}
		if len(vals) == 1 {
			email := strings.TrimSpace(vals[0])
			if strings.Contains(email, ",") {
				return Identity{}, false, errConflict
			}
			if email == "" || len(email) > 254 || !strings.Contains(email, "@") {
				return Identity{}, false, ErrUnauthorized
			}
			u, err := a.forwardAuthUser(ctx, email)
			if err != nil {
				return Identity{}, false, err
			}
			if err := accept(u, MethodForwardAuth); err != nil {
				return Identity{}, false, err
			}
		}
	}
	return id, found, nil
}

// peerTrusted tests the immediate TCP peer against the trusted CIDRs. It reads
// r.RemoteAddr only — never X-Forwarded-For or any other header (research.md §2).
func (a *Authenticator) peerTrusted(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range a.fwdTrusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// forwardAuthUser resolves the asserted email to a user, auto-provisioning one on first
// sight. Provisioned users have Source=forward_auth, no password, and are NOT admins:
// an operator promotes them deliberately. A concurrent first request races on
// CreateUser; the loser re-reads.
func (a *Authenticator) forwardAuthUser(ctx context.Context, email string) (*domain.User, error) {
	u, err := a.store.GetUserByEmail(ctx, email)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("auth: forward-auth user lookup: %w", err)
	}
	u = &domain.User{
		ID:        newID("usr_"),
		Email:     email,
		Source:    domain.UserSourceForwardAuth,
		CreatedAt: a.now().UTC(),
	}
	if err := a.store.CreateUser(ctx, u); err != nil {
		if !errors.Is(err, store.ErrConflict) {
			return nil, fmt.Errorf("auth: provision forward-auth user: %w", err)
		}
		if u, err = a.store.GetUserByEmail(ctx, email); err != nil {
			return nil, fmt.Errorf("auth: forward-auth user re-read: %w", err)
		}
		return u, nil
	}
	a.log.InfoContext(ctx, "auth: provisioned forward-auth user", "user_id", u.ID)
	return u, nil
}
