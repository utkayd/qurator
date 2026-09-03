package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

// Logging emits one structured line per request. It deliberately omits the client
// address and the full query string: the address is never persisted (FR-022) and query
// strings on /v1/qr carry user content.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		slog.InfoContext(r.Context(), "http",
			"method", r.Method,
			"route", r.Pattern,
			"status", sw.status,
			"bytes", sw.bytes,
			"duration_ms", float64(time.Since(start).Microseconds())/1000,
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
	wrote  bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	s.wrote = true
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
