package v1

import (
	"net/http"

	"github.com/utkayd/qurator/internal/httpapi"
)

// ExportHandler serves the Export routes. Foundation stub: the owning Stage 2 stream
// replaces the body of this file and MUST keep the constructor name so wiring in
// cmd/qurator does not change.
type ExportHandler struct{}

// NewExportHandler constructs the handler. Stream: fill in real dependencies here.
func NewExportHandler() *ExportHandler { return &ExportHandler{} }

// ServeHTTP implements http.Handler.
func (h *ExportHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	httpapi.NotImplemented().ServeHTTP(w, r)
}
