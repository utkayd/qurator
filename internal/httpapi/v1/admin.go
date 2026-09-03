package v1

import (
	"net/http"

	"github.com/utkayd/qurator/internal/httpapi"
)

// AdminHandler serves the Admin routes. Foundation stub: the owning Stage 2 stream
// replaces the body of this file and MUST keep the constructor name so wiring in
// cmd/qurator does not change.
type AdminHandler struct{}

// NewAdminHandler constructs the handler. Stream: fill in real dependencies here.
func NewAdminHandler() *AdminHandler { return &AdminHandler{} }

// ServeHTTP implements http.Handler.
func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	httpapi.NotImplemented().ServeHTTP(w, r)
}
