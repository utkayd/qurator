package v1

import (
	"net/http"

	"github.com/utkayd/qurator/internal/httpapi"
)

// QRHandler serves the QR routes. Foundation stub: the owning Stage 2 stream
// replaces the body of this file and MUST keep the constructor name so wiring in
// cmd/qurator does not change.
type QRHandler struct{}

// NewQRHandler constructs the handler. Stream: fill in real dependencies here.
func NewQRHandler() *QRHandler { return &QRHandler{} }

// ServeHTTP implements http.Handler.
func (h *QRHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	httpapi.NotImplemented().ServeHTTP(w, r)
}
