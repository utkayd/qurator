package v1

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/utkayd/qurator/internal/auth"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi"
	"github.com/utkayd/qurator/internal/store"
)

// TokensHandler serves GET/POST /v1/tokens and DELETE /v1/tokens/{id}.
type TokensHandler struct {
	auth  *auth.Authenticator
	store store.Store
	mux   *http.ServeMux
}

// NewTokensHandler constructs the handler. Every route requires authentication.
func NewTokensHandler(a *auth.Authenticator, st store.Store) *TokensHandler {
	h := &TokensHandler{auth: a, store: st, mux: http.NewServeMux()}
	h.mux.Handle("GET /v1/tokens", auth.RequireAuth(http.HandlerFunc(h.list)))
	h.mux.Handle("POST /v1/tokens", auth.RequireAuth(http.HandlerFunc(h.create)))
	h.mux.Handle("DELETE /v1/tokens/{id}", auth.RequireAuth(http.HandlerFunc(h.revoke)))
	return h
}

// ServeHTTP implements http.Handler.
func (h *TokensHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

// tokenSummary is the OpenAPI ApiTokenSummary schema — it has no field for the secret.
type tokenSummary struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

// tokenCreated is ApiTokenCreated: the only shape that ever carries the secret.
type tokenCreated struct {
	tokenSummary
	Secret string `json:"secret"`
}

func toTokenSummary(t *domain.APIToken) tokenSummary {
	return tokenSummary{ID: t.ID, Name: t.Name, CreatedAt: t.CreatedAt.UTC(), LastUsedAt: t.LastUsedAt, RevokedAt: t.RevokedAt, ExpiresAt: t.ExpiresAt}
}

func (h *TokensHandler) list(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.IdentityFrom(r.Context())
	toks, err := h.store.ListTokens(r.Context(), id.UserID)
	if err != nil {
		httpapi.Internal(w, r, err)
		return
	}
	out := make([]tokenSummary, 0, len(toks))
	for _, t := range toks {
		out = append(out, toTokenSummary(t))
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func (h *TokensHandler) create(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.IdentityFrom(r.Context())
	var body struct {
		Name      string     `json:"name"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || len(body.Name) > 128 {
		httpapi.WriteError(w, httpapi.CodeInvalidRequest, "Token name must be between 1 and 128 characters.", map[string]any{"field": "name"})
		return
	}
	if body.ExpiresAt != nil && !body.ExpiresAt.After(time.Now()) {
		httpapi.WriteError(w, httpapi.CodeInvalidRequest, "expires_at must be in the future.", map[string]any{"field": "expires_at"})
		return
	}
	t, secret, err := h.auth.CreateToken(r.Context(), id.UserID, body.Name, body.ExpiresAt)
	if err != nil {
		httpapi.Internal(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusCreated, tokenCreated{tokenSummary: toTokenSummary(t), Secret: secret})
}

// revoke is idempotent: an already-revoked token is still 204. A token that does not
// exist or belongs to someone else is 404 — never 403, which would confirm existence.
func (h *TokensHandler) revoke(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.IdentityFrom(r.Context())
	err := h.store.RevokeToken(r.Context(), r.PathValue("id"), id.UserID)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, store.ErrNotFound):
		httpapi.WriteError(w, httpapi.CodeNotFound, "No such token.", nil)
	default:
		httpapi.Internal(w, r, err)
	}
}
