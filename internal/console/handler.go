package console

import (
	"net/http"

	"github.com/utkayd/qurator/internal/httpapi"
)

// Handler serves the embedded web console under /ui/. Foundation stub; Stream E fills it.
type Handler struct{}

// New constructs the console handler. Stream E: add template and service dependencies here.
func New() *Handler { return &Handler{} }

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	httpapi.NotImplemented().ServeHTTP(w, r)
}
