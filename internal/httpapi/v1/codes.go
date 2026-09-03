package v1

import (
	"net/http"

	"github.com/utkayd/qurator/internal/httpapi"
)

// CodesHandler serves the Codes routes. Foundation stub: the owning Stage 2 stream
// replaces the body of this file and MUST keep the constructor name so wiring in
// cmd/qurator does not change.
type CodesHandler struct{}

// NewCodesHandler constructs the handler. Stream: fill in real dependencies here.
func NewCodesHandler() *CodesHandler { return &CodesHandler{} }

// ServeHTTP implements http.Handler.
func (h *CodesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	httpapi.NotImplemented().ServeHTTP(w, r)
}
