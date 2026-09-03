package v1

import (
	"net/http"

	"github.com/utkayd/qurator/internal/httpapi"
)

// AuthHandler serves the Auth routes. Foundation stub: the owning Stage 2 stream
// replaces the body of this file and MUST keep the constructor name so wiring in
// cmd/qurator does not change.
type AuthHandler struct{}

// NewAuthHandler constructs the handler. Stream: fill in real dependencies here.
func NewAuthHandler() *AuthHandler { return &AuthHandler{} }

// ServeHTTP implements http.Handler.
func (h *AuthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	httpapi.NotImplemented().ServeHTTP(w, r)
}
