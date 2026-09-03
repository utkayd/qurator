package v1

import (
	"net/http"

	"github.com/utkayd/qurator/internal/httpapi"
)

// TokensHandler serves the Tokens routes. Foundation stub: the owning Stage 2 stream
// replaces the body of this file and MUST keep the constructor name so wiring in
// cmd/qurator does not change.
type TokensHandler struct{}

// NewTokensHandler constructs the handler. Stream: fill in real dependencies here.
func NewTokensHandler() *TokensHandler { return &TokensHandler{} }

// ServeHTTP implements http.Handler.
func (h *TokensHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	httpapi.NotImplemented().ServeHTTP(w, r)
}
