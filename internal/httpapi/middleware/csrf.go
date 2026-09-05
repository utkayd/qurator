package middleware

import (
	"net/http"

	"github.com/utkayd/qurator/internal/httpapi"
)

// CSRFHeader is the custom header every cookie-authenticated mutating request must carry.
// A cross-site HTML form cannot set a custom header, which closes the classic vector
// (research.md §5). Bearer-authenticated requests are CSRF-immune and exempt.
const CSRFHeader = "X-Qurator-Requested-With"

// AuthMethodFunc reports how the request was authenticated: "cookie", "bearer",
// "forward_auth", or "" for anonymous. Supplied by the auth package so this middleware
// does not import it.
type AuthMethodFunc func(r *http.Request) string

// CSRF rejects mutating requests authenticated by cookie that lack the custom header.
func CSRF(method AuthMethodFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			if method(r) == "cookie" && r.Header.Get(CSRFHeader) == "" {
				httpapi.WriteError(w, httpapi.CodeForbidden,
					"Cookie-authenticated requests must include the "+CSRFHeader+" header.", nil)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
