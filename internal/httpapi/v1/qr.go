package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/utkayd/qurator/internal/config"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi"
	"github.com/utkayd/qurator/internal/qr"
)

// IdentityFunc reports whether the request carries an authenticated identity. The auth
// middleware (another package) puts identity in the request context; this indirection
// keeps the QR handler free of any auth type.
type IdentityFunc func(*http.Request) bool

// maxJSONBody bounds the POST body: the largest payload is 2953 bytes and the JSON
// envelope around it is small. Raised in US5 for logos.
const maxJSONBody = 64 << 10

// QRHandler serves GET and POST /v1/qr — ephemeral generation (FR-001..FR-006).
//
// The constructor deliberately accepts neither a Store nor a BlobStore: this path can
// not create a record because it has nothing to create one with (Principle III).
type QRHandler struct {
	renderer *qr.Renderer
	cfg      config.EphemeralConfig
	isAuthed IdentityFunc
	serve    http.Handler
}

// NewQRHandler constructs the handler. When cfg.Public is false every request must be
// authenticated (isAuthed). When it is true anonymous requests are accepted and the
// whole handler is wrapped in limiter (FR-006).
func NewQRHandler(renderer *qr.Renderer, cfg config.EphemeralConfig, isAuthed IdentityFunc, limiter func(http.Handler) http.Handler) *QRHandler {
	h := &QRHandler{renderer: renderer, cfg: cfg, isAuthed: isAuthed}
	var inner http.Handler = http.HandlerFunc(h.handle)
	if cfg.Public && limiter != nil {
		inner = limiter(inner)
	}
	h.serve = inner
	return h
}

// ServeHTTP implements http.Handler.
func (h *QRHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.Public && (h.isAuthed == nil || !h.isAuthed(r)) {
		httpapi.WriteError(w, httpapi.CodeUnauthorized, "A valid credential is required.", nil)
		return
	}
	h.serve.ServeHTTP(w, r)
}

func (h *QRHandler) handle(w http.ResponseWriter, r *http.Request) {
	var (
		opts qr.Options
		err  error
	)
	switch r.Method {
	case http.MethodGet:
		opts, err = parseQuery(r)
	case http.MethodPost:
		opts, err = parseBody(r)
	default:
		w.Header().Set("Allow", "GET, POST")
		httpapi.WriteError(w, httpapi.CodeInvalidRequest, "Method not allowed.", nil)
		return
	}
	if err != nil {
		writeQRError(w, r, err)
		return
	}
	res, err := h.renderer.Render(r.Context(), opts)
	if err != nil {
		writeQRError(w, r, err)
		return
	}
	writeImage(w, r, res)
}

// writeImage sends the rendered bytes with immutable caching headers (FR-004). The
// ETag is the content hash, so identical output from GET and POST shares one tag.
func writeImage(w http.ResponseWriter, r *http.Request, res *qr.Result) {
	sum := sha256.Sum256(res.Bytes)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	hdr := w.Header()
	hdr.Set("ETag", etag)
	hdr.Set("Cache-Control", "public, max-age=31536000, immutable")
	hdr.Set("X-Content-Type-Options", "nosniff")
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	hdr.Set("Content-Type", res.ContentType)
	hdr.Set("Content-Length", strconv.Itoa(len(res.Bytes)))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(res.Bytes); err != nil {
		// The client went away mid-body; nothing useful to do.
		return
	}
}

// etagMatches implements the If-None-Match comparison (strong tags, weak prefix
// tolerated, "*" matches anything).
func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, tag := range strings.Split(header, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" {
			return true
		}
		tag = strings.TrimPrefix(tag, "W/")
		if tag == etag {
			return true
		}
	}
	return false
}

// ---- request parsing -----------------------------------------------------------------

// fieldError is a validation failure on one named field; it becomes invalid_request.
type fieldError struct {
	field string
	msg   string
}

func (e *fieldError) Error() string { return e.field + ": " + e.msg }

var hexColorRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// stylingRequest mirrors OpenAPI StylingRequest. Pointers distinguish "absent" from a
// zero value.
type stylingRequest struct {
	FgColor       *string `json:"fg_color"`
	BgColor       *string `json:"bg_color"`
	ModuleShape   *string `json:"module_shape"`
	MarginModules *int    `json:"margin_modules"`
	SizePx        *int    `json:"size_px"`
	ECLevel       *string `json:"ec_level"`
}

// generateRequest mirrors OpenAPI GenerateQrRequest.
type generateRequest struct {
	Content string          `json:"content"`
	Format  *string         `json:"format"`
	Styling *stylingRequest `json:"styling"`
}

func parseQuery(r *http.Request) (qr.Options, error) {
	q := r.URL.Query()
	str := func(k string) *string {
		if !q.Has(k) {
			return nil
		}
		v := q.Get(k)
		return &v
	}
	num := func(k string) (*int, error) {
		if !q.Has(k) {
			return nil, nil
		}
		n, err := strconv.Atoi(q.Get(k))
		if err != nil {
			return nil, &fieldError{k, "must be an integer"}
		}
		return &n, nil
	}
	req := generateRequest{Content: q.Get("content"), Format: str("format"), Styling: &stylingRequest{
		FgColor:     str("fg_color"),
		BgColor:     str("bg_color"),
		ModuleShape: str("module_shape"),
		ECLevel:     str("ec_level"),
	}}
	var err error
	if req.Styling.MarginModules, err = num("margin_modules"); err != nil {
		return qr.Options{}, err
	}
	if req.Styling.SizePx, err = num("size_px"); err != nil {
		return qr.Options{}, err
	}
	return req.toOptions()
}

func parseBody(r *http.Request) (qr.Options, error) {
	ct := r.Header.Get("Content-Type")
	if mt, _, _ := strings.Cut(ct, ";"); strings.TrimSpace(mt) != "application/json" {
		return qr.Options{}, &fieldError{"content-type", "must be application/json"}
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody+1))
	dec.DisallowUnknownFields()
	var req generateRequest
	if err := dec.Decode(&req); err != nil {
		return qr.Options{}, &fieldError{"body", "malformed JSON: " + jsonReason(err)}
	}
	if dec.More() {
		return qr.Options{}, &fieldError{"body", "trailing data after the JSON object"}
	}
	return req.toOptions()
}

// jsonReason keeps decoder messages that are safe to echo (they describe the client's
// own input) and hides anything else.
func jsonReason(err error) string {
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	switch {
	case errors.As(err, &se):
		return "syntax error at offset " + strconv.FormatInt(se.Offset, 10)
	case errors.As(err, &te):
		return "wrong type for field " + te.Field
	case strings.HasPrefix(err.Error(), "json: unknown field"):
		return err.Error()
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected end of input"
	}
	return "invalid"
}

// toOptions validates against the OpenAPI schema and produces render options. Range
// checks that are schema-level (minimums, enums) are made here; instance-level bounds
// (max pixels, payload cap) are the renderer's and yield their own error codes.
func (g generateRequest) toOptions() (qr.Options, error) {
	if g.Content == "" {
		return qr.Options{}, &fieldError{"content", "is required"}
	}
	o := qr.Options{Content: []byte(g.Content), Format: qr.FormatPNG}
	if g.Format != nil {
		switch qr.Format(*g.Format) {
		case qr.FormatPNG, qr.FormatSVG:
			o.Format = qr.Format(*g.Format)
		default:
			return o, &fieldError{"format", "must be png or svg"}
		}
	}
	s := g.Styling
	if s == nil {
		s = &stylingRequest{}
	}
	if s.FgColor != nil {
		if !hexColorRe.MatchString(*s.FgColor) {
			return o, &fieldError{"fg_color", "must be #RRGGBB"}
		}
		o.FgColor = *s.FgColor
	}
	if s.BgColor != nil {
		if !hexColorRe.MatchString(*s.BgColor) {
			return o, &fieldError{"bg_color", "must be #RRGGBB"}
		}
		o.BgColor = *s.BgColor
	}
	if s.ModuleShape != nil {
		switch domain.ModuleShape(*s.ModuleShape) {
		case domain.ShapeSquare, domain.ShapeDot, domain.ShapeRounded:
			o.Shape = domain.ModuleShape(*s.ModuleShape)
		default:
			return o, &fieldError{"module_shape", "must be square, dot or rounded"}
		}
	}
	o.Margin = 4
	if s.MarginModules != nil {
		if *s.MarginModules < 4 || *s.MarginModules > 64 {
			return o, &fieldError{"margin_modules", "must be between 4 and 64"}
		}
		o.Margin = *s.MarginModules
	}
	o.SizePx = 512
	if s.SizePx != nil {
		if *s.SizePx < 64 {
			return o, &fieldError{"size_px", "must be at least 64"}
		}
		o.SizePx = *s.SizePx
	}
	o.ECLevel = domain.ECMedium
	if s.ECLevel != nil {
		switch domain.ECLevel(*s.ECLevel) {
		case domain.ECLow, domain.ECMedium, domain.ECQuartile, domain.ECHigh:
			o.ECLevel = domain.ECLevel(*s.ECLevel)
		default:
			return o, &fieldError{"ec_level", "must be L, M, Q or H"}
		}
	}
	return o, nil
}

// ---- error mapping ---------------------------------------------------------------------

// writeQRError maps every error the parser or renderer can produce onto the stable
// catalogue in contracts/errors.md, with structured details.
func writeQRError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		fe  *fieldError
		ctl *qr.ContentTooLargeError
		de  *qr.DimensionsExceededError
		te  *qr.RenderTimeoutError
		ioe *qr.InvalidOptionError
	)
	switch {
	case errors.As(err, &fe):
		httpapi.WriteError(w, httpapi.CodeInvalidRequest, fmt.Sprintf("Parameter '%s' %s.", fe.field, fe.msg), map[string]any{"field": fe.field})
	case errors.As(err, &ioe):
		httpapi.WriteError(w, httpapi.CodeInvalidRequest, fmt.Sprintf("Parameter '%s' is invalid: %s.", ioe.Field, ioe.Reason), map[string]any{"field": ioe.Field})
	case errors.As(err, &ctl):
		httpapi.WriteError(w, httpapi.CodeContentTooLarge,
			fmt.Sprintf("Content is %d bytes; the maximum at error correction level %s is %d bytes.", ctl.Actual, ctl.Level, ctl.Limit),
			map[string]any{"limit_bytes": ctl.Limit, "actual_bytes": ctl.Actual, "ec_level": string(ctl.Level)})
	case errors.As(err, &de):
		httpapi.WriteError(w, httpapi.CodeDimensionsExceeded,
			fmt.Sprintf("Requested size %dpx exceeds the configured maximum of %dpx.", de.Requested, de.Maximum),
			map[string]any{"requested": de.Requested, "maximum": de.Maximum})
	case errors.As(err, &te):
		httpapi.WriteError(w, httpapi.CodeRenderTimeout,
			fmt.Sprintf("Rendering exceeded the %dms budget.", te.Timeout.Milliseconds()),
			map[string]any{"timeout_ms": te.Timeout.Milliseconds()})
	case errors.Is(err, context.Canceled):
		// The client disconnected; there is nobody to answer.
		return
	default:
		httpapi.Internal(w, r, err)
	}
}
