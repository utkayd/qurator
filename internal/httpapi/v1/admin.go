package v1

import (
	"errors"
	"net/http"

	"github.com/utkayd/qurator/internal/auth"
	"github.com/utkayd/qurator/internal/httpapi"
	"github.com/utkayd/qurator/internal/store"
)

// AdminHandler serves DELETE /v1/admin/aliases/{alias}.
type AdminHandler struct {
	store store.Store
	mux   *http.ServeMux
}

// NewAdminHandler constructs the handler. Every route requires an administrator.
func NewAdminHandler(st store.Store) *AdminHandler {
	h := &AdminHandler{store: st, mux: http.NewServeMux()}
	h.mux.Handle("DELETE /v1/admin/aliases/{alias}", auth.RequireAdmin(http.HandlerFunc(h.releaseAlias)))
	return h
}

// ServeHTTP implements http.Handler.
func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

// releaseAlias frees a short code reserved by a deleted code (FR-018 escape hatch).
func (h *AdminHandler) releaseAlias(w http.ResponseWriter, r *http.Request) {
	alias := r.PathValue("alias")
	err := h.store.ReleaseAlias(r.Context(), alias)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, store.ErrNotFound):
		httpapi.WriteError(w, httpapi.CodeNotFound, "No reservation exists for that alias.", map[string]any{"alias": alias})
	case errors.Is(err, store.ErrConflict):
		httpapi.WriteError(w, httpapi.CodeConflict, "The alias belongs to a live code; delete the code first.", map[string]any{"alias": alias})
	default:
		httpapi.Internal(w, r, err)
	}
}
