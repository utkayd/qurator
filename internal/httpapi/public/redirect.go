package public

import (
	"net/http"

	"github.com/utkayd/qurator/internal/httpapi"
)

// PublicHandler serves the Public routes. Foundation stub: the owning Stage 2 stream
// replaces the body of this file and MUST keep the constructor name so wiring in
// cmd/qurator does not change.
type PublicHandler struct{}

// NewPublicHandler constructs the handler. Stream: fill in real dependencies here.
func NewPublicHandler() *PublicHandler { return &PublicHandler{} }

// ServeHTTP implements http.Handler.
func (h *PublicHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	httpapi.NotImplemented().ServeHTTP(w, r)
}
