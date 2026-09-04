// Package observability — see specs/001-qr-service-baseline/plan.md for its role and boundaries.
package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
)

// ctxKey is an unexported type for context keys defined in this package,
// avoiding collisions with keys defined in other packages.
type ctxKey int

const requestIDKey ctxKey = iota

// NewRequestID returns a new random request identifier: 16 random bytes,
// hex-encoded (32 hex characters).
func NewRequestID() string {
	b := make([]byte, 16)
	// crypto/rand.Read never returns an error on supported platforms; if it
	// somehow fails, we fall back to an all-zero ID rather than panicking.
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// WithRequestID returns a copy of ctx carrying the given request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFrom extracts the request ID stored in ctx, if any.
func RequestIDFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(requestIDKey).(string)
	return id, ok
}

// ctxHandler wraps a slog.Handler so that Handle automatically appends a
// request_id attribute when one is present in the context. This lets callers
// use slog.InfoContext(ctx, ...) etc. without ever threading a per-request
// logger through the call stack.
type ctxHandler struct {
	slog.Handler
}

// Handle implements slog.Handler.
func (h ctxHandler) Handle(ctx context.Context, r slog.Record) error {
	if id, ok := RequestIDFrom(ctx); ok {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs implements slog.Handler.
func (h ctxHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return ctxHandler{Handler: h.Handler.WithAttrs(attrs)}
}

// WithGroup implements slog.Handler.
func (h ctxHandler) WithGroup(name string) slog.Handler {
	return ctxHandler{Handler: h.Handler.WithGroup(name)}
}

// NewLogger builds a *slog.Logger writing to w in either "json" or "text"
// format, at the given level ("debug", "info", "warn", "error"). The
// returned logger's handler automatically injects a request_id attribute for
// any record logged with a context carrying one (see WithRequestID).
func NewLogger(level, format string, w io.Writer) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}

	var base slog.Handler
	switch format {
	case "text":
		base = slog.NewTextHandler(w, opts)
	default:
		base = slog.NewJSONHandler(w, opts)
	}

	return slog.New(ctxHandler{Handler: base})
}

type routePatternKey struct{}

// WithRoutePattern records the pattern a route was registered under. The router calls
// this at registration so metrics and logs label by the authoritative pattern even if a
// nested mux later rewrites r.Pattern.
func WithRoutePattern(ctx context.Context, pattern string) context.Context {
	return context.WithValue(ctx, routePatternKey{}, pattern)
}

// RoutePattern returns the registered pattern for the request: the context value set by
// the router, else r.Pattern, else "unmatched". Never the concrete path — that would be
// one metric series per short code.
func RoutePattern(r *http.Request) string {
	if p, ok := r.Context().Value(routePatternKey{}).(string); ok && p != "" {
		return p
	}
	if r.Pattern != "" {
		return r.Pattern
	}
	return "unmatched"
}
