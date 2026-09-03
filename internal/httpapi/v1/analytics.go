package v1

import (
	"net/http"

	"github.com/utkayd/qurator/internal/httpapi"
)

// AnalyticsHandler serves the Analytics routes. Foundation stub: the owning Stage 2 stream
// replaces the body of this file and MUST keep the constructor name so wiring in
// cmd/qurator does not change.
type AnalyticsHandler struct{}

// NewAnalyticsHandler constructs the handler. Stream: fill in real dependencies here.
func NewAnalyticsHandler() *AnalyticsHandler { return &AnalyticsHandler{} }

// ServeHTTP implements http.Handler.
func (h *AnalyticsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	httpapi.NotImplemented().ServeHTTP(w, r)
}
