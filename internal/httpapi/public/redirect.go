package public

import (
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/utkayd/qurator/internal/blob"
	"github.com/utkayd/qurator/internal/codes"
	"github.com/utkayd/qurator/internal/domain"
)

// NoStore is the exact Cache-Control value on every scan response. 302 alone is not a
// strong enough guarantee against heuristic caching; this header does the real work
// (research.md §4). Never 301/308.
const NoStore = "no-store, no-cache, must-revalidate"

// ClassifyFunc turns a User-Agent into the coarse fields a ScanEvent records. The
// analytics stream supplies the real parser; the default records "unknown".
type ClassifyFunc func(userAgent string) (family string, device domain.DeviceCategory, isBot bool)

func defaultClassify(string) (string, domain.DeviceCategory, bool) {
	return "", domain.DeviceUnknown, false
}

// Options are PublicHandler's dependencies. Nothing here can authenticate: the public
// routes are mounted without auth middleware and this handler never looks for one
// (Principle IV).
type Options struct {
	Resolver codes.Resolver
	Blob     blob.BlobStore
	// Recorder receives one event per scan of a known code. Nil means NopRecorder.
	Recorder domain.Recorder
	// FallbackDestination, when set, is where unknown/disabled/deleted codes redirect
	// instead of rendering the landing page (FR-014).
	FallbackDestination string
	// Classify parses the User-Agent; nil records unknown.
	Classify ClassifyFunc
	// Now is injectable for tests.
	Now func() time.Time
}

// PublicHandler serves GET /r/{code} and GET /i/{file}.
type PublicHandler struct {
	resolver codes.Resolver
	blob     blob.BlobStore
	recorder domain.Recorder
	fallback string
	classify ClassifyFunc
	now      func() time.Time
}

// NewPublicHandler constructs the handler.
func NewPublicHandler(o Options) *PublicHandler {
	h := &PublicHandler{
		resolver: o.Resolver,
		blob:     o.Blob,
		recorder: o.Recorder,
		fallback: strings.TrimSpace(o.FallbackDestination),
		classify: o.Classify,
		now:      o.Now,
	}
	if h.recorder == nil {
		h.recorder = domain.NopRecorder{}
	}
	if h.classify == nil {
		h.classify = defaultClassify
	}
	if h.now == nil {
		h.now = func() time.Time { return time.Now().UTC() }
	}
	return h
}

// ServeHTTP dispatches on the matched route pattern.
func (h *PublicHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Pattern {
	case "GET /r/{code}":
		h.redirect(w, r)
	case "GET /i/{file}":
		h.image(w, r)
	default:
		http.NotFound(w, r)
	}
}

func setNoStore(h http.Header) {
	h.Set("Cache-Control", NoStore)
	h.Set("Pragma", "no-cache")
	h.Set("Expires", "0")
}

// redirect resolves the code (cache → at most one store lookup, FR-017) and answers
// with a 302 for active codes or the landing response otherwise (FR-014). It never
// reads X-Forwarded-For and never logs the client address (FR-022).
func (h *PublicHandler) redirect(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	res, err := h.resolver.Resolve(r.Context(), code)
	if err != nil {
		slog.ErrorContext(r.Context(), "scan: resolve failed", "err", err)
		h.landing(w, r)
		return
	}
	if res.Found && res.State != domain.CodeDeleted {
		h.record(r, res.CodeID)
	}
	if res.Found && res.State == domain.CodeActive {
		setNoStore(w.Header())
		w.Header().Set("Location", res.Destination)
		w.WriteHeader(http.StatusFound)
		return
	}
	h.landing(w, r)
}

// record hands the event to the recorder, which must not block (Principle IV).
func (h *PublicHandler) record(r *http.Request, codeID string) {
	family, device, bot := h.classify(r.UserAgent())
	h.recorder.Record(domain.ScanEvent{
		CodeID:         codeID,
		OccurredAt:     h.now(),
		UAFamily:       family,
		DeviceCategory: device,
		ReferrerHost:   referrerHost(r.Referer()),
		IsBot:          bot,
	})
}

// referrerHost keeps only the host of the Referer: never the path, never the query.
func referrerHost(ref string) string {
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// landing is the non-error response for unknown, disabled, and deleted codes alike, so
// the scan path never distinguishes which codes have existed.
func (h *PublicHandler) landing(w http.ResponseWriter, r *http.Request) {
	setNoStore(w.Header())
	if h.fallback != "" {
		w.Header().Set("Location", h.fallback)
		w.WriteHeader(http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if err := landingTmpl.Execute(w, nil); err != nil {
		slog.DebugContext(r.Context(), "scan: writing landing page", "err", err)
	}
}

var landingTmpl = template.Must(template.New("landing").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>This code is not available</title>
<style>
  body { margin: 0; font-family: system-ui, -apple-system, "Segoe UI", Roboto, sans-serif; background: #f6f7f9; color: #1f2937; }
  main { max-width: 32rem; margin: 15vh auto; padding: 2rem; background: #fff; border-radius: 12px; box-shadow: 0 1px 3px rgba(0,0,0,.08); }
  h1 { font-size: 1.4rem; margin: 0 0 .75rem; }
  p { margin: 0; line-height: 1.5; color: #4b5563; }
</style>
</head>
<body>
<main>
  <h1>This code is not available</h1>
  <p>The QR code you scanned does not lead anywhere right now. It may have been retired, paused, or printed with a code that was never registered. If you expected something here, please contact whoever gave you the code.</p>
</main>
</body>
</html>
`))
