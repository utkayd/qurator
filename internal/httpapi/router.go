package httpapi

import (
	"net/http"

	"github.com/utkayd/qurator/internal/observability"
)

// Middleware is the standard wrapping shape.
type Middleware func(http.Handler) http.Handler

// Handlers are the per-stream handler sets. Each Stage 2 stream fills its own; the
// foundation supplies stubs. Nil entries fall back to notImplemented.
type Handlers struct {
	Public    http.Handler // /r/{code}, /i/{id}.{ext}
	QR        http.Handler // /v1/qr
	Codes     http.Handler // /v1/codes*
	Auth      http.Handler // /v1/auth/*
	Tokens    http.Handler // /v1/tokens*
	Admin     http.Handler // /v1/admin/*
	Analytics http.Handler // /v1/codes/{id}/analytics
	Export    http.Handler // /v1/export
	Console   http.Handler // /ui/*
	Healthz   http.Handler
	Readyz    http.Handler
}

// Options configures the two route groups.
//
// Common middleware runs on EVERY request (request ID, metrics, recovery, logging).
// Auth runs ONLY on the protected group. The public group never sees it — this is
// Constitution Principle IV made structural, and router_test.go asserts it.
type Options struct {
	Common []Middleware
	// PerRoute wraps each registered handler individually, so middleware that reads
	// r.Pattern (metrics, logging) sees the LEAF pattern ("GET /r/{code}") rather than
	// the group prefix ("/v1/") that a nested ServeMux reports to outer middleware.
	PerRoute []Middleware
	Auth     Middleware
	CSRF     Middleware // cookie-session CSRF check; protected group only
	// SigninLimiter, when set, wraps ONLY "POST /v1/auth/signin". That route lives in
	// the public group (it must be reachable anonymously) yet accepts a password, so it
	// is the one public route that needs a brute-force limiter. It is applied innermost,
	// so PerRoute middleware still observes rejected attempts.
	SigninLimiter Middleware
}

// SigninPattern is the one public route that takes a password; Options.SigninLimiter
// is applied to it alone.
const SigninPattern = "POST /v1/auth/signin"

// Routes is the full route table from contracts/openapi.yaml plus the console.
// Every entry is registered even when its handler is a stub, so Stage 2 streams fill
// handlers rather than editing this file.
var Routes = []Route{
	// Public — NO auth middleware, ever.
	{Group: GroupPublic, Pattern: "GET /r/{code}", Handler: "Public"},
	{Group: GroupPublic, Pattern: "GET /i/{file}", Handler: "Public"},
	{Group: GroupPublic, Pattern: "GET /healthz", Handler: "Healthz"},
	{Group: GroupPublic, Pattern: "GET /readyz", Handler: "Readyz"},
	{Group: GroupPublic, Pattern: SigninPattern, Handler: "Auth"},

	// Protected — full chain.
	{Group: GroupProtected, Pattern: "GET /v1/qr", Handler: "QR"},
	{Group: GroupProtected, Pattern: "POST /v1/qr", Handler: "QR"},
	{Group: GroupProtected, Pattern: "GET /v1/codes", Handler: "Codes"},
	{Group: GroupProtected, Pattern: "POST /v1/codes", Handler: "Codes"},
	{Group: GroupProtected, Pattern: "GET /v1/codes/{id}", Handler: "Codes"},
	{Group: GroupProtected, Pattern: "PATCH /v1/codes/{id}", Handler: "Codes"},
	{Group: GroupProtected, Pattern: "DELETE /v1/codes/{id}", Handler: "Codes"},
	{Group: GroupProtected, Pattern: "POST /v1/codes/{id}/disable", Handler: "Codes"},
	{Group: GroupProtected, Pattern: "POST /v1/codes/{id}/enable", Handler: "Codes"},
	{Group: GroupProtected, Pattern: "GET /v1/codes/{id}/analytics", Handler: "Analytics"},
	{Group: GroupProtected, Pattern: "POST /v1/auth/signout", Handler: "Auth"},
	{Group: GroupProtected, Pattern: "GET /v1/auth/me", Handler: "Auth"},
	{Group: GroupProtected, Pattern: "GET /v1/tokens", Handler: "Tokens"},
	{Group: GroupProtected, Pattern: "POST /v1/tokens", Handler: "Tokens"},
	{Group: GroupProtected, Pattern: "DELETE /v1/tokens/{id}", Handler: "Tokens"},
	{Group: GroupProtected, Pattern: "DELETE /v1/admin/aliases/{alias}", Handler: "Admin"},
	{Group: GroupProtected, Pattern: "GET /v1/export", Handler: "Export"},

	// Console — protected group; the console handler renders its own sign-in page for
	// anonymous requests, so /ui/ is mounted with auth in "optional" mode by the stream.
	{Group: GroupConsole, Pattern: "/ui/", Handler: "Console"},
}

// Group names a route group.
type Group string

const (
	GroupPublic    Group = "public"
	GroupProtected Group = "protected"
	GroupConsole   Group = "console"
)

// Route is one registered pattern.
type Route struct {
	Group   Group
	Pattern string
	Handler string // field name in Handlers
}

// NewRouter builds the root handler. The ephemeral endpoint (/v1/qr) sits in the
// protected group; when config makes it public the QR handler itself decides to accept
// anonymous identities — the auth middleware attaches identity but does not reject on
// that route (see auth stream). Public scan routes are a separate mux that the auth
// middleware is never applied to.
func NewRouter(h Handlers, o Options) http.Handler {
	public := http.NewServeMux()
	protected := http.NewServeMux()
	console := http.NewServeMux()

	for _, rt := range Routes {
		hd := withPattern(rt.Pattern, h.lookup(rt.Handler))
		if rt.Pattern == SigninPattern && o.SigninLimiter != nil {
			hd = o.SigninLimiter(hd)
		}
		for i := len(o.PerRoute) - 1; i >= 0; i-- {
			hd = o.PerRoute[i](hd)
		}
		switch rt.Group {
		case GroupPublic:
			public.Handle(rt.Pattern, hd)
		case GroupProtected:
			protected.Handle(rt.Pattern, hd)
		case GroupConsole:
			console.Handle(rt.Pattern, hd)
		}
	}

	var protectedChain http.Handler = protected
	if o.CSRF != nil {
		protectedChain = o.CSRF(protectedChain)
	}
	if o.Auth != nil {
		protectedChain = o.Auth(protectedChain)
	}
	var consoleChain http.Handler = console
	if o.Auth != nil {
		consoleChain = o.Auth(consoleChain)
	}

	root := http.NewServeMux()
	root.Handle("/r/", public)
	root.Handle("/i/", public)
	root.Handle("/healthz", public)
	root.Handle("/readyz", public)
	root.Handle(SigninPattern, public)
	root.Handle("/v1/", protectedChain)
	root.Handle("/ui/", consoleChain)
	root.Handle("/", http.RedirectHandler("/ui/", http.StatusFound))

	var out http.Handler = root
	for i := len(o.Common) - 1; i >= 0; i-- {
		out = o.Common[i](out)
	}
	return out
}

func (h Handlers) lookup(name string) http.Handler {
	var hd http.Handler
	switch name {
	case "Public":
		hd = h.Public
	case "QR":
		hd = h.QR
	case "Codes":
		hd = h.Codes
	case "Auth":
		hd = h.Auth
	case "Tokens":
		hd = h.Tokens
	case "Admin":
		hd = h.Admin
	case "Analytics":
		hd = h.Analytics
	case "Export":
		hd = h.Export
	case "Console":
		hd = h.Console
	case "Healthz":
		hd = h.Healthz
	case "Readyz":
		hd = h.Readyz
	}
	if hd == nil {
		return NotImplemented()
	}
	return hd
}

// NotImplemented is the foundation stub for every route a stream has not filled yet.
func NotImplemented() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, CodeNotImplemented, "This endpoint is not implemented yet.", map[string]any{"route": r.Pattern})
	})
}

// withPattern stamps the registered pattern into the request context so
// observability.RoutePattern is authoritative regardless of nested muxes.
func withPattern(pattern string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(observability.WithRoutePattern(r.Context(), pattern)))
	})
}
