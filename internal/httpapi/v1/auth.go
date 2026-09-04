package v1

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/utkayd/qurator/internal/auth"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi"
	"github.com/utkayd/qurator/internal/store"
)

// maxJSONBody bounds every auth/token request body.
const maxJSONBody = 64 << 10

// AuthHandler serves POST /v1/auth/signin, POST /v1/auth/signout, GET /v1/auth/me.
type AuthHandler struct {
	auth  *auth.Authenticator
	store store.Store
	mux   *http.ServeMux
}

// NewAuthHandler constructs the handler.
func NewAuthHandler(a *auth.Authenticator, st store.Store) *AuthHandler {
	h := &AuthHandler{auth: a, store: st, mux: http.NewServeMux()}
	h.mux.HandleFunc("POST /v1/auth/signin", h.signin)
	h.mux.Handle("POST /v1/auth/signout", auth.RequireAuth(http.HandlerFunc(h.signout)))
	h.mux.Handle("GET /v1/auth/me", auth.RequireAuth(http.HandlerFunc(h.me)))
	return h
}

// ServeHTTP implements http.Handler.
func (h *AuthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

// userJSON is the OpenAPI User schema.
type userJSON struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	IsAdmin     bool       `json:"is_admin"`
	Source      string     `json:"source"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at"`
}

func toUserJSON(u *domain.User) userJSON {
	return userJSON{ID: u.ID, Email: u.Email, IsAdmin: u.IsAdmin, Source: string(u.Source), CreatedAt: u.CreatedAt.UTC(), LastLoginAt: u.LastLoginAt}
}

// decodeJSON reads a bounded, strict JSON body. It writes the 400 itself and returns
// false when the body is unusable.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		httpapi.WriteError(w, httpapi.CodeInvalidRequest, "The request body is not valid JSON for this endpoint.", nil)
		return false
	}
	if dec.More() {
		httpapi.WriteError(w, httpapi.CodeInvalidRequest, "The request body must contain a single JSON object.", nil)
		return false
	}
	return true
}

func (h *AuthHandler) signin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.Email = strings.TrimSpace(body.Email)
	if body.Email == "" || body.Password == "" {
		httpapi.WriteError(w, httpapi.CodeInvalidRequest, "Both email and password are required.", map[string]any{"field": "email,password"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")

	// Uniform failure: unknown email, password-less (forward-auth) account, and wrong
	// password all cost one Argon2 verification and return the same 401.
	unauthorized := func() {
		httpapi.WriteError(w, httpapi.CodeUnauthorized, "The email or password is incorrect.", nil)
	}
	u, err := h.store.GetUserByEmail(r.Context(), body.Email)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			httpapi.Internal(w, r, err)
			return
		}
		auth.DummyVerify(body.Password)
		unauthorized()
		return
	}
	if u.PasswordHash == "" {
		auth.DummyVerify(body.Password)
		unauthorized()
		return
	}
	ok, err := auth.VerifyPassword(u.PasswordHash, body.Password)
	if err != nil {
		httpapi.Internal(w, r, err)
		return
	}
	if !ok {
		unauthorized()
		return
	}
	token, exp, err := h.auth.IssueSession(u)
	if err != nil {
		httpapi.Internal(w, r, err)
		return
	}
	http.SetCookie(w, h.auth.SessionCookie(token, exp))
	httpapi.WriteJSON(w, http.StatusOK, toUserJSON(u))
}

func (h *AuthHandler) signout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, auth.ClearSessionCookie())
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (h *AuthHandler) me(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.IdentityFrom(r.Context())
	u, err := h.store.GetUserByID(r.Context(), id.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpapi.WriteError(w, httpapi.CodeUnauthorized, "The presented credential is not valid.", nil)
			return
		}
		httpapi.Internal(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusOK, toUserJSON(u))
}
