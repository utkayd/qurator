package v1

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/utkayd/qurator/internal/config"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi"
	"github.com/utkayd/qurator/internal/qr"
)

// AuthedFunc reports whether the request carries an authenticated identity. The auth
// middleware (another package) puts identity in the request context; this indirection
// keeps the QR handler free of any auth type.
type AuthedFunc func(*http.Request) bool

// maxBody bounds the POST body. The largest content payload is 2953 bytes; the rest
// of the budget is a base64 logo (qr.MaxLogoBytes encoded, plus a third for base64).
const maxBody = 64<<10 + qr.MaxLogoBytes*4/3

// maxMultipartMemory is how much of a multipart body is held in memory before parts
// spill to disk; sized so a legal request never touches disk.
const maxMultipartMemory = maxBody

// QRHandler serves GET and POST /v1/qr — ephemeral generation (FR-001..FR-006).
//
// The constructor deliberately accepts neither a Store nor a BlobStore: this path can
// not create a record because it has nothing to create one with (Principle III).
type QRHandler struct {
	renderer *qr.Renderer
	cfg      config.EphemeralConfig
	isAuthed AuthedFunc
	serve    http.Handler
}

// NewQRHandler constructs the handler. When cfg.Public is false every request must be
// authenticated (isAuthed). When it is true anonymous requests are accepted and the
// whole handler is wrapped in limiter (FR-006).
func NewQRHandler(renderer *qr.Renderer, cfg config.EphemeralConfig, isAuthed AuthedFunc, limiter func(http.Handler) http.Handler) *QRHandler {
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
		opts, err = parseBody(w, r)
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
	// res.Bytes is the rendered QR image; Content-Type (an image type) and
	// X-Content-Type-Options: nosniff are set above before any bytes are
	// written, so this body can never be sniffed or executed as HTML/script
	// even though it is derived from caller-controlled QR content.
	if _, err := w.Write(res.Bytes); err != nil { //nolint:gosec // image body written after Content-Type + nosniff are set; not HTML-injectable
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

// logoSpec mirrors OpenAPI LogoSpec. auto_raise is an extension: the contract says a
// logo raises the effective level "automatically as needed" (FR-027); the flag lets a
// caller who wants the requested level respected exactly opt out (default true).
type logoSpec struct {
	ImageBase64 string   `json:"image_base64"`
	Scale       *float64 `json:"scale"`
	AutoRaise   *bool    `json:"auto_raise"`
}

// qrStylingRequest mirrors OpenAPI StylingRequest. Pointers distinguish "absent" from a
// zero value.
type qrStylingRequest struct {
	FgColor       *string   `json:"fg_color"`
	BgColor       *string   `json:"bg_color"`
	ModuleShape   *string   `json:"module_shape"`
	MarginModules *int      `json:"margin_modules"`
	SizePx        *int      `json:"size_px"`
	ECLevel       *string   `json:"ec_level"`
	Logo          *logoSpec `json:"logo"`
}

// defaultLogoScale applies when a logo is sent without a scale.
const defaultLogoScale = 0.15

// generateRequest mirrors OpenAPI GenerateQrRequest.
type generateRequest struct {
	Content string            `json:"content"`
	Format  *string           `json:"format"`
	Styling *qrStylingRequest `json:"styling"`
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
	req := generateRequest{Content: q.Get("content"), Format: str("format"), Styling: &qrStylingRequest{
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

func parseBody(w http.ResponseWriter, r *http.Request) (qr.Options, error) {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return qr.Options{}, &fieldError{"content-type", "is missing or malformed"}
	}
	switch mt {
	case "application/json":
		return parseJSON(r)
	case "multipart/form-data":
		return parseMultipart(w, r)
	}
	return qr.Options{}, &fieldError{"content-type", "must be application/json or multipart/form-data"}
}

func parseJSON(r *http.Request) (qr.Options, error) {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody+1))
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

// parseMultipart accepts the same fields as the query form plus a `logo` file part
// (with optional `logo_scale` and `logo_auto_raise` fields). It exists so a browser
// form or curl can send a logo without base64-encoding it.
func parseMultipart(w http.ResponseWriter, r *http.Request) (qr.Options, error) {
	// Bound both the raw body and the in-memory multipart parse to maxBody
	// (which derives from qr.MaxLogoBytes) so a client cannot exhaust memory
	// with an oversized or maliciously-crafted multipart body.
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseMultipartForm(maxMultipartMemory); err != nil { //nolint:gosec // r.Body is already bounded by http.MaxBytesReader(w, r.Body, maxBody) above, and maxMultipartMemory==maxBody derives from qr.MaxLogoBytes
		return qr.Options{}, &fieldError{"body", "malformed or oversized multipart body"}
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	f := r.MultipartForm.Value
	str := func(k string) *string {
		if v, ok := f[k]; ok && len(v) > 0 {
			return &v[0]
		}
		return nil
	}
	num := func(k string) (*int, error) {
		s := str(k)
		if s == nil {
			return nil, nil
		}
		n, err := strconv.Atoi(*s)
		if err != nil {
			return nil, &fieldError{k, "must be an integer"}
		}
		return &n, nil
	}
	req := generateRequest{Format: str("format"), Styling: &qrStylingRequest{
		FgColor:     str("fg_color"),
		BgColor:     str("bg_color"),
		ModuleShape: str("module_shape"),
		ECLevel:     str("ec_level"),
	}}
	if c := str("content"); c != nil {
		req.Content = *c
	}
	var err error
	if req.Styling.MarginModules, err = num("margin_modules"); err != nil {
		return qr.Options{}, err
	}
	if req.Styling.SizePx, err = num("size_px"); err != nil {
		return qr.Options{}, err
	}
	opts, err := req.toOptions()
	if err != nil {
		return opts, err
	}
	if files := r.MultipartForm.File["logo"]; len(files) > 0 {
		fh, err := files[0].Open()
		if err != nil {
			return opts, &fieldError{"logo", "could not be read"}
		}
		defer func() { _ = fh.Close() }()
		data, err := io.ReadAll(io.LimitReader(fh, qr.MaxLogoBytes+1))
		if err != nil {
			return opts, &fieldError{"logo", "could not be read"}
		}
		if len(data) > qr.MaxLogoBytes {
			return opts, &fieldError{"logo", fmt.Sprintf("must be at most %d bytes", qr.MaxLogoBytes)}
		}
		logo := &qr.Logo{Image: data, Scale: defaultLogoScale, AutoRaise: true}
		if s := str("logo_scale"); s != nil {
			v, err := strconv.ParseFloat(*s, 64)
			if err != nil {
				return opts, &fieldError{"logo_scale", "must be a number"}
			}
			logo.Scale = v
		}
		if s := str("logo_auto_raise"); s != nil {
			v, err := strconv.ParseBool(*s)
			if err != nil {
				return opts, &fieldError{"logo_auto_raise", "must be true or false"}
			}
			logo.AutoRaise = v
		}
		if err := validateLogo(logo); err != nil {
			return opts, err
		}
		opts.Logo = logo
	}
	return opts, nil
}

// validateLogo applies the schema range for scale.
func validateLogo(l *qr.Logo) error {
	if l.Scale < 0.01 || l.Scale > qr.MaxLogoScale {
		return &fieldError{"logo.scale", fmt.Sprintf("must be between 0.01 and %.2f", qr.MaxLogoScale)}
	}
	return nil
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
		s = &qrStylingRequest{}
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
	if s.Logo != nil {
		logo, err := s.Logo.decode()
		if err != nil {
			return o, err
		}
		o.Logo = logo
	}
	return o, nil
}

// decode validates the JSON logo spec and produces the renderer's Logo. Both /v1/qr and
// POST /v1/codes go through it so a logo is accepted or rejected identically on each.
func (l *logoSpec) decode() (*qr.Logo, error) {
	if l.ImageBase64 == "" {
		return nil, &fieldError{"logo.image_base64", "is required"}
	}
	if len(l.ImageBase64) > qr.MaxLogoBytes*4/3+4 {
		return nil, &fieldError{"logo.image_base64", fmt.Sprintf("must decode to at most %d bytes", qr.MaxLogoBytes)}
	}
	data, err := base64.StdEncoding.DecodeString(l.ImageBase64)
	if err != nil {
		return nil, &fieldError{"logo.image_base64", "is not valid base64"}
	}
	logo := &qr.Logo{Image: data, Scale: defaultLogoScale, AutoRaise: true}
	if l.Scale != nil {
		logo.Scale = *l.Scale
	}
	if l.AutoRaise != nil {
		logo.AutoRaise = *l.AutoRaise
	}
	if err := validateLogo(logo); err != nil {
		return nil, err
	}
	return logo, nil
}

// ---- error mapping ---------------------------------------------------------------------

// writeQRError maps every error the parser or renderer can produce onto the stable
// catalogue in contracts/errors.md, with structured details.
func writeQRError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) {
		// The client disconnected; there is nobody to answer.
		return
	}
	d, ok := qrErrorDetail(err)
	if !ok {
		httpapi.Internal(w, r, err)
		return
	}
	httpapi.WriteError(w, d.Code, d.Message, d.Details)
}

// qrErrorDetail maps the renderer's typed errors onto the stable catalogue. ok is false
// for anything that is not a renderer validation error (an internal failure).
func qrErrorDetail(err error) (d httpapi.ErrorDetail, ok bool) {
	var (
		fe  *fieldError
		ctl *qr.ContentTooLargeError
		de  *qr.DimensionsExceededError
		te  *qr.RenderTimeoutError
		ioe *qr.InvalidOptionError
		ce  *qr.ContrastTooLowError
		le  *qr.LogoTooLargeError
	)
	switch {
	case errors.As(err, &ce):
		return httpapi.ErrorDetail{Code: httpapi.CodeContrastTooLow,
			Message: fmt.Sprintf("Foreground/background contrast is %.2f:1; at least %.1f:1 is required.", ce.Ratio, ce.Minimum),
			Details: map[string]any{"ratio": ce.Ratio, "minimum": ce.Minimum}}, true
	case errors.As(err, &le):
		return httpapi.ErrorDetail{Code: httpapi.CodeLogoTooLarge,
			Message: fmt.Sprintf("Logo covers %.0f%% of the symbol; the maximum at error correction level %s is %.0f%%.", le.Scale*100, le.Level, le.MaxScale*100),
			Details: map[string]any{"scale": le.Scale, "max_scale": le.MaxScale, "ec_level": string(le.Level)}}, true
	case errors.As(err, &fe):
		return httpapi.ErrorDetail{Code: httpapi.CodeInvalidRequest, Message: fmt.Sprintf("Parameter '%s' %s.", fe.field, fe.msg), Details: map[string]any{"field": fe.field}}, true
	case errors.As(err, &ioe):
		return httpapi.ErrorDetail{Code: httpapi.CodeInvalidRequest, Message: fmt.Sprintf("Parameter '%s' is invalid: %s.", ioe.Field, ioe.Reason), Details: map[string]any{"field": ioe.Field}}, true
	case errors.As(err, &ctl):
		return httpapi.ErrorDetail{Code: httpapi.CodeContentTooLarge,
			Message: fmt.Sprintf("Content is %d bytes; the maximum at error correction level %s is %d bytes.", ctl.Actual, ctl.Level, ctl.Limit),
			Details: map[string]any{"limit_bytes": ctl.Limit, "actual_bytes": ctl.Actual, "ec_level": string(ctl.Level)}}, true
	case errors.As(err, &de):
		return httpapi.ErrorDetail{Code: httpapi.CodeDimensionsExceeded,
			Message: fmt.Sprintf("Requested size %dpx exceeds the configured maximum of %dpx.", de.Requested, de.Maximum),
			Details: map[string]any{"requested": de.Requested, "maximum": de.Maximum}}, true
	case errors.As(err, &te):
		return httpapi.ErrorDetail{Code: httpapi.CodeRenderTimeout,
			Message: fmt.Sprintf("Rendering exceeded the %dms budget.", te.Timeout.Milliseconds()),
			Details: map[string]any{"timeout_ms": te.Timeout.Milliseconds()}}, true
	}
	return httpapi.ErrorDetail{}, false
}
