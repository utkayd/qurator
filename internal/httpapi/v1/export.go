package v1

import (
	"log/slog"
	"net/http"

	"github.com/utkayd/qurator/internal/export"
	"github.com/utkayd/qurator/internal/httpapi"
	"github.com/utkayd/qurator/internal/store"
)

// IsAdminFunc reports whether r carries an already-authenticated admin identity. It is
// injected rather than imported from the auth package so that this handler (Stream F)
// does not need to depend on the auth stream's concrete identity type — the auth
// middleware that runs ahead of this handler in the protected chain is what actually
// establishes the identity; this func only reads what it left on the request.
type IsAdminFunc func(*http.Request) bool

// ExportHandler serves GET /v1/export: a streaming tar dump of the whole instance
// (FR-055, FR-056), gated to admins only, on top of whatever the auth middleware already
// established for the request.
type ExportHandler struct {
	Store   store.Store
	IsAdmin IsAdminFunc
}

// NewExportHandler constructs the handler. isAdmin may be nil only in tests that do not
// exercise the admin gate; production wiring must supply a real check.
func NewExportHandler(st store.Store, isAdmin IsAdminFunc) *ExportHandler {
	return &ExportHandler{Store: st, IsAdmin: isAdmin}
}

// ServeHTTP implements http.Handler.
func (h *ExportHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpapi.WriteError(w, httpapi.CodeInvalidRequest, "This endpoint only supports GET.", nil)
		return
	}
	if h.Store == nil {
		httpapi.NotImplemented().ServeHTTP(w, r)
		return
	}
	if h.IsAdmin == nil || !h.IsAdmin(r) {
		httpapi.WriteError(w, httpapi.CodeForbidden, "Export requires an admin identity.", nil)
		return
	}

	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", `attachment; filename="qurator-export.tar"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	if f, ok := w.(http.Flusher); ok {
		defer f.Flush()
	}
	if err := export.Write(r.Context(), h.Store, w); err != nil {
		// The 200 and headers are already on the wire; all we can do now is log and
		// let the client observe a truncated archive.
		slog.ErrorContext(r.Context(), "export: streaming write failed", "err", err, "route", r.Pattern)
	}
}
