package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/utkayd/qurator/internal/httpapi"
)

// Recover converts a panic into a bland 500. The stack goes to the log with the request
// ID; the client sees only the error envelope (FR-044).
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler { //nolint:errorlint // sentinel compared by identity per net/http docs
					panic(rec)
				}
				slog.ErrorContext(r.Context(), "panic", "value", rec, "stack", string(debug.Stack()))
				httpapi.WriteError(w, httpapi.CodeInternal, "An unexpected error occurred.", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
