// Package middleware holds the cross-cutting HTTP wrappers used by both route groups.
package middleware

import (
	"net/http"

	"github.com/utkayd/qurator/internal/observability"
)

// RequestID assigns a fresh request ID to every request and echoes it in the response so
// a user can quote it when reporting a problem. Incoming IDs are NOT trusted: accepting
// a client-supplied value lets an attacker forge correlation in logs.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := observability.NewRequestID()
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(observability.WithRequestID(r.Context(), id)))
	})
}
