package v1

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/utkayd/qurator/internal/codes"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi"
	"github.com/utkayd/qurator/internal/qr"
	"github.com/utkayd/qurator/internal/store"
)

// IdentityFunc extracts the authenticated user from the request. The auth middleware
// (Stream C) supplies the real one from its context value; tests supply a header reader.
type IdentityFunc func(r *http.Request) (userID string, ok bool)

// CodesHandler serves every /v1/codes* route (contracts/openapi.yaml, tags: codes).
type CodesHandler struct {
	svc      *codes.Service
	identity IdentityFunc
}

// NewCodesHandler constructs the handler.
func NewCodesHandler(svc *codes.Service, identity IdentityFunc) *CodesHandler {
	return &CodesHandler{svc: svc, identity: identity}
}

const (
	// maxBatchBodyBytes caps POST /v1/codes/batch: codes.batch_max items, each possibly
	// carrying a base64 logo, need far more than a single create's budget.
	maxBatchBodyBytes = 16 << 20
	defaultPageLimit  = 50
	maxPageLimit      = 200
	maxCursorLen      = 512
)

var ifMatchRe = regexp.MustCompile(`^"([0-9]+)"$`)

// ServeHTTP dispatches on the matched route pattern.
func (h *CodesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.identity(r)
	if !ok {
		httpapi.WriteError(w, httpapi.CodeUnauthorized, "Authentication is required.", nil)
		return
	}
	switch r.Pattern {
	case "GET /v1/codes":
		h.list(w, r, userID)
	case "POST /v1/codes":
		h.create(w, r, userID)
	case "POST /v1/codes/batch":
		h.createBatch(w, r, userID)
	case "GET /v1/codes/{id}":
		h.get(w, r, userID)
	case "PATCH /v1/codes/{id}":
		h.patch(w, r, userID)
	case "DELETE /v1/codes/{id}":
		h.delete(w, r, userID)
	case "POST /v1/codes/{id}/disable":
		h.setState(w, r, userID, domain.CodeDisabled)
	case "POST /v1/codes/{id}/enable":
		h.setState(w, r, userID, domain.CodeActive)
	default:
		httpapi.WriteError(w, httpapi.CodeNotFound, "No such route.", nil)
	}
}

// ---- wire shapes ---------------------------------------------------------------------

// stylingRequest mirrors OpenAPI StylingRequest for dynamic codes. The logo spec is the
// one /v1/qr uses (qr.go) so both paths accept identical input.
type stylingRequest struct {
	FgColor       *string   `json:"fg_color"`
	BgColor       *string   `json:"bg_color"`
	ModuleShape   *string   `json:"module_shape"`
	MarginModules *int      `json:"margin_modules"`
	SizePx        *int      `json:"size_px"`
	ECLevel       *string   `json:"ec_level"`
	Logo          *logoSpec `json:"logo"`
}

type createCodeRequest struct {
	ClientRef   string          `json:"client_ref"`
	Mode        string          `json:"mode"`
	Destination string          `json:"destination"`
	Alias       string          `json:"alias"`
	Styling     *stylingRequest `json:"styling"`
}

type batchRequest struct {
	Items []createCodeRequest `json:"items"`
}

// batchItemResponse is one entry of the batch response: exactly one of code / error is
// set, matched by status (spec 003, FR-205).
type batchItemResponse struct {
	Index  int                  `json:"index"`
	Status string               `json:"status"`
	Code   *codeResponse        `json:"code,omitempty"`
	Error  *httpapi.ErrorDetail `json:"error,omitempty"`
}

type batchResponse struct {
	Results []batchItemResponse `json:"results"`
}

type updateDestinationRequest struct {
	Destination string `json:"destination"`
}

type stylingProfile struct {
	FgColor          string `json:"fg_color"`
	BgColor          string `json:"bg_color"`
	ModuleShape      string `json:"module_shape"`
	MarginModules    int    `json:"margin_modules"`
	SizePx           int    `json:"size_px"`
	ECLevel          string `json:"ec_level"`
	ECLevelEffective string `json:"ec_level_effective"`
	HasLogo          bool   `json:"has_logo"`
}

// codeResponse is the Code schema. scan_url is present for dynamic codes only: a direct
// code's printed image encodes the destination, so offering a scan address would
// misdescribe what scanning it does (FR-106). storage_url is present only when the blob
// store can derive one and client_ref only when the caller supplied one (spec 003). Every
// optional key is omitted, not nulled.
type codeResponse struct {
	ID          string         `json:"id"`
	Mode        string         `json:"mode"`
	ShortCode   string         `json:"short_code"`
	Version     int64          `json:"version"`
	IsAlias     bool           `json:"is_alias"`
	Destination string         `json:"destination"`
	State       string         `json:"state"`
	Styling     stylingProfile `json:"styling"`
	ImageURL    string         `json:"image_url"`
	StorageURL  string         `json:"storage_url,omitempty"`
	ClientRef   string         `json:"client_ref,omitempty"`
	ScanURL     string         `json:"scan_url,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type codePage struct {
	Items      []codeResponse `json:"items"`
	NextCursor *string        `json:"next_cursor"`
}

func (h *CodesHandler) toResponse(ctx context.Context, c *domain.Code) codeResponse {
	mode := c.Mode
	if mode == "" {
		mode = domain.ModeDynamic
	}
	scanURL := ""
	if mode != domain.ModeDirect {
		scanURL = h.svc.ScanURL(c.ShortCode)
	}
	storageURL, _ := h.svc.StorageURL(ctx, c)
	return codeResponse{
		ID:          c.ID,
		Mode:        string(mode),
		ShortCode:   c.ShortCode,
		Version:     c.Version,
		IsAlias:     c.IsAlias,
		Destination: c.Destination,
		State:       string(c.State),
		Styling: stylingProfile{
			FgColor:          c.Styling.FgColor,
			BgColor:          c.Styling.BgColor,
			ModuleShape:      string(c.Styling.ModuleShape),
			MarginModules:    c.Styling.MarginModules,
			SizePx:           c.Styling.SizePx,
			ECLevel:          string(c.Styling.ECLevel),
			ECLevelEffective: string(c.Styling.ECLevelEffective),
			HasLogo:          c.Styling.LogoBlobKey != "",
		},
		ImageURL:   h.svc.ImageURL(ctx, c),
		StorageURL: storageURL,
		ClientRef:  c.ClientRef,
		ScanURL:    scanURL,
		CreatedAt:  c.CreatedAt.UTC(),
		UpdatedAt:  c.UpdatedAt.UTC(),
	}
}

var hexColorRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// stylingFromRequest validates the StylingRequest schema bounds; the renderer applies
// the deeper checks (contrast, logo area) it owns. The decoded logo bytes and the
// auto_raise flag travel beside the Styling because neither is persisted as-is.
func stylingFromRequest(in *stylingRequest) (out domain.Styling, logo []byte, autoRaise bool, field string, ok bool) {
	if in == nil {
		return out, nil, false, "", true
	}
	if in.Logo != nil {
		l, err := in.Logo.decode()
		if err != nil {
			var fe *fieldError
			if errors.As(err, &fe) {
				return out, nil, false, "styling." + fe.field, false
			}
			return out, nil, false, "styling.logo", false
		}
		logo, autoRaise = l.Image, l.AutoRaise
		out.LogoScale = l.Scale
	}
	if in.FgColor != nil {
		if !hexColorRe.MatchString(*in.FgColor) {
			return out, nil, false, "styling.fg_color", false
		}
		out.FgColor = *in.FgColor
	}
	if in.BgColor != nil {
		if !hexColorRe.MatchString(*in.BgColor) {
			return out, nil, false, "styling.bg_color", false
		}
		out.BgColor = *in.BgColor
	}
	if in.ModuleShape != nil {
		switch domain.ModuleShape(*in.ModuleShape) {
		case domain.ShapeSquare, domain.ShapeDot, domain.ShapeRounded:
			out.ModuleShape = domain.ModuleShape(*in.ModuleShape)
		default:
			return out, nil, false, "styling.module_shape", false
		}
	}
	if in.MarginModules != nil {
		if *in.MarginModules < 4 || *in.MarginModules > 64 {
			return out, nil, false, "styling.margin_modules", false
		}
		out.MarginModules = *in.MarginModules
	}
	if in.SizePx != nil {
		if *in.SizePx < 64 || *in.SizePx > 4096 {
			return out, nil, false, "styling.size_px", false
		}
		out.SizePx = *in.SizePx
	}
	if in.ECLevel != nil {
		switch domain.ECLevel(*in.ECLevel) {
		case domain.ECLow, domain.ECMedium, domain.ECQuartile, domain.ECHigh:
			out.ECLevel = domain.ECLevel(*in.ECLevel)
		default:
			return out, nil, false, "styling.ec_level", false
		}
	}
	return out, logo, autoRaise, "", true
}

// isRenderError reports whether err came from the renderer's own validation (contrast,
// logo budget, size, payload, timeout). These share /v1/qr's error mapping.
func isRenderError(err error) bool {
	return errors.Is(err, qr.ErrContrastTooLow) ||
		errors.Is(err, qr.ErrLogoTooLarge) ||
		errors.Is(err, qr.ErrDimensionsExceeded) ||
		errors.Is(err, qr.ErrRenderTimeout) ||
		errors.Is(err, qr.ErrContentTooLarge) ||
		errors.Is(err, qr.ErrInvalidOption)
}

// writeServiceError maps service and store errors onto the stable catalogue.
func writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) {
		return // the client disconnected; there is nobody to answer
	}
	d, ok := serviceErrorDetail(err)
	if !ok {
		httpapi.Internal(w, r, err)
		return
	}
	httpapi.WriteError(w, d.Code, d.Message, d.Details)
}

// serviceErrorDetail is the single mapping from service/store errors to the error
// envelope, shared by the whole-request path and the per-item batch path. ok is false
// for an internal failure, which the callers log with the request context.
func serviceErrorDetail(err error) (d httpapi.ErrorDetail, ok bool) {
	var ve *codes.ValidationError
	details := map[string]any(nil)
	if errors.As(err, &ve) {
		details = ve.Details
	}
	var ce *codes.ConflictError
	var cre *codes.ClientRefConflictError
	e := func(code httpapi.ErrorCode, msg string, det map[string]any) (httpapi.ErrorDetail, bool) {
		return httpapi.ErrorDetail{Code: code, Message: msg, Details: det}, true
	}
	switch {
	case isRenderError(err):
		return qrErrorDetail(err)
	case errors.As(err, &ce):
		return e(httpapi.CodeConflict, "The code was modified by another request; re-read it and retry with its current version.",
			map[string]any{"expected": ce.Expected, "actual": ce.Actual})
	case errors.As(err, &cre):
		return e(httpapi.CodeClientRefConflict, "This client_ref was already used for a code with a different destination or mode.",
			map[string]any{"client_ref": cre.ClientRef, "existing_id": cre.ExistingID})
	case errors.Is(err, codes.ErrClientRefInvalid):
		return e(httpapi.CodeInvalidRequest, "client_ref must be at most 128 characters and unique within the batch.", details)
	case errors.Is(err, codes.ErrDirectImmutable):
		return e(httpapi.CodeDirectCodeImmutable,
			"This is a direct code: its destination is encoded in the printed image and cannot be changed, disabled, or enabled. Create a new code instead.", details)
	case errors.Is(err, codes.ErrInvalidMode):
		return e(httpapi.CodeInvalidRequest, "mode must be one of dynamic, direct.", details)
	case errors.Is(err, codes.ErrUnsupportedScheme):
		return e(httpapi.CodeUnsupportedScheme, "The destination uses a scheme this instance does not permit.", details)
	case errors.Is(err, codes.ErrSelfReferential):
		return e(httpapi.CodeSelfReferentialDestination, "The destination points back at this instance's scan path.", nil)
	case errors.Is(err, codes.ErrInvalidDestination):
		return e(httpapi.CodeInvalidRequest, "The destination is not a valid absolute URL.", details)
	case errors.Is(err, codes.ErrAliasReserved):
		return e(httpapi.CodeAliasReserved, "That alias is reserved by this instance.", details)
	case errors.Is(err, codes.ErrAliasInvalid):
		return e(httpapi.CodeAliasInvalid, "The alias does not meet the character set, length, or shape rules.", details)
	case errors.Is(err, store.ErrAliasTaken):
		return e(httpapi.CodeAliasTaken, "That alias is already in use or reserved by a deleted code.", details)
	case errors.Is(err, store.ErrClientRefTaken):
		return e(httpapi.CodeClientRefConflict, "This client_ref was claimed by a concurrent request; re-read your codes and retry.", details)
	case errors.Is(err, store.ErrNotFound):
		return e(httpapi.CodeNotFound, "No such code.", nil)
	case errors.Is(err, store.ErrConflict):
		return e(httpapi.CodeConflict, "The code is in a state that does not allow this change.", nil)
	}
	return httpapi.ErrorDetail{}, false
}

// ---- operations ----------------------------------------------------------------------

// inputFromRequest validates the wire shape of one CreateCodeRequest and turns it into
// the service input. Failures are the schema-level invalid_request envelopes; everything
// deeper is the service's business.
func inputFromRequest(req createCodeRequest, userID string) (codes.CreateInput, *httpapi.ErrorDetail) {
	bad := func(msg, field string) (codes.CreateInput, *httpapi.ErrorDetail) {
		return codes.CreateInput{}, &httpapi.ErrorDetail{Code: httpapi.CodeInvalidRequest, Message: msg, Details: map[string]any{"field": field}}
	}
	if strings.TrimSpace(req.Destination) == "" {
		return bad("A destination is required.", "destination")
	}
	switch domain.CodeMode(req.Mode) {
	case "", domain.ModeDynamic, domain.ModeDirect:
	default:
		return bad("mode must be one of dynamic, direct.", "mode")
	}
	if len(req.ClientRef) > codes.MaxClientRefLen {
		return codes.CreateInput{}, &httpapi.ErrorDetail{Code: httpapi.CodeInvalidRequest, Message: "client_ref must be at most 128 characters.",
			Details: map[string]any{"field": "client_ref", "max_length": codes.MaxClientRefLen}}
	}
	styling, logo, autoRaise, field, ok := stylingFromRequest(req.Styling)
	if !ok {
		return bad("A styling parameter is out of range or unsupported.", field)
	}
	return codes.CreateInput{
		UserID:        userID,
		Mode:          domain.CodeMode(req.Mode),
		Destination:   req.Destination,
		Alias:         req.Alias,
		Styling:       styling,
		Logo:          logo,
		LogoAutoRaise: autoRaise,
		ClientRef:     req.ClientRef,
	}, nil
}

func (h *CodesHandler) create(w http.ResponseWriter, r *http.Request, userID string) {
	var req createCodeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	in, bad := inputFromRequest(req, userID)
	if bad != nil {
		httpapi.WriteError(w, bad.Code, bad.Message, bad.Details)
		return
	}
	if in.ClientRef != "" {
		// A single create with a client_ref is a batch of one: same idempotency rules
		// (FR-206), so a retry returns the existing code with 200 instead of a duplicate.
		res := h.svc.CreateBatch(r.Context(), userID, []codes.CreateInput{in})[0]
		switch res.Status {
		case codes.BatchError:
			writeServiceError(w, r, res.Err)
		case codes.BatchExisting:
			w.Header().Set("Location", "/v1/codes/"+res.Code.ID)
			httpapi.WriteJSON(w, http.StatusOK, h.toResponse(r.Context(), res.Code))
		default:
			w.Header().Set("Location", "/v1/codes/"+res.Code.ID)
			httpapi.WriteJSON(w, http.StatusCreated, h.toResponse(r.Context(), res.Code))
		}
		return
	}
	c, err := h.svc.Create(r.Context(), in)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/codes/"+c.ID)
	httpapi.WriteJSON(w, http.StatusCreated, h.toResponse(r.Context(), c))
}

// createBatch serves POST /v1/codes/batch (spec 003, FR-205). Only the batch's shape can
// fail the request as a whole (empty → 400, oversized → 413); every item gets its own
// result and the response is 200 whenever the batch was processed.
func (h *CodesHandler) createBatch(w http.ResponseWriter, r *http.Request, userID string) {
	var req batchRequest
	if !decodeJSONLimit(w, r, &req, maxBatchBodyBytes) {
		return
	}
	if len(req.Items) == 0 {
		httpapi.WriteError(w, httpapi.CodeInvalidRequest, "A batch needs at least one item.", map[string]any{"field": "items"})
		return
	}
	if limit := h.svc.BatchMax(); len(req.Items) > limit {
		httpapi.WriteError(w, httpapi.CodeBatchTooLarge, "The batch has more items than this instance accepts in one request.",
			map[string]any{"limit": limit, "actual": len(req.Items)})
		return
	}
	// Schema-level validation per item; items that fail never reach the service, but
	// they keep their index so the response stays positional.
	out := batchResponse{Results: make([]batchItemResponse, len(req.Items))}
	inputs := make([]codes.CreateInput, 0, len(req.Items))
	inputIdx := make([]int, 0, len(req.Items))
	for i, item := range req.Items {
		in, bad := inputFromRequest(item, userID)
		if bad != nil {
			out.Results[i] = batchItemResponse{Index: i, Status: string(codes.BatchError), Error: bad}
			continue
		}
		inputs = append(inputs, in)
		inputIdx = append(inputIdx, i)
	}
	if len(inputs) > 0 {
		for _, res := range h.svc.CreateBatch(r.Context(), userID, inputs) {
			i := inputIdx[res.Index]
			item := batchItemResponse{Index: i, Status: string(res.Status)}
			if res.Status == codes.BatchError {
				d, ok := serviceErrorDetail(res.Err)
				if !ok {
					slog.ErrorContext(r.Context(), "batch item failed", "index", i, "err", res.Err, "route", r.Pattern)
					d = httpapi.ErrorDetail{Code: httpapi.CodeInternal, Message: "An unexpected error occurred."}
				}
				item.Error = &d
			} else {
				c := h.toResponse(r.Context(), res.Code)
				item.Code = &c
			}
			out.Results[i] = item
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func (h *CodesHandler) get(w http.ResponseWriter, r *http.Request, userID string) {
	c, err := h.svc.Get(r.Context(), r.PathValue("id"), userID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, h.toResponse(r.Context(), c))
}

func (h *CodesHandler) list(w http.ResponseWriter, r *http.Request, userID string) {
	q := r.URL.Query()
	f := domain.CodeFilter{UserID: userID, Limit: defaultPageLimit}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxPageLimit {
			httpapi.WriteError(w, httpapi.CodeInvalidRequest, "limit must be an integer between 1 and 200.", map[string]any{"field": "limit"})
			return
		}
		f.Limit = n
	}
	if v := q.Get("cursor"); v != "" {
		if len(v) > maxCursorLen {
			httpapi.WriteError(w, httpapi.CodeInvalidRequest, "The cursor is not valid.", map[string]any{"field": "cursor"})
			return
		}
		if _, err := base64.RawURLEncoding.DecodeString(v); err != nil {
			httpapi.WriteError(w, httpapi.CodeInvalidRequest, "The cursor is not valid.", map[string]any{"field": "cursor"})
			return
		}
		f.Cursor = v
	}
	for _, p := range []struct {
		name string
		dst  **time.Time
	}{{"created_after", &f.CreatedAfter}, {"created_before", &f.CreatedBefore}} {
		if v := q.Get(p.name); v != "" {
			t, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				httpapi.WriteError(w, httpapi.CodeInvalidRequest, p.name+" must be an RFC 3339 timestamp.", map[string]any{"field": p.name})
				return
			}
			t = t.UTC()
			*p.dst = &t
		}
	}
	f.Destination = q.Get("destination_contains")

	items, next, err := h.svc.List(r.Context(), f)
	if err != nil {
		// Every driver (and the reference memstore) reports an undecodable cursor with
		// this phrase; the cursor is opaque so the handler cannot pre-validate further.
		if f.Cursor != "" && strings.Contains(err.Error(), "invalid cursor") {
			httpapi.WriteError(w, httpapi.CodeInvalidRequest, "The cursor is not valid.", map[string]any{"field": "cursor"})
			return
		}
		writeServiceError(w, r, err)
		return
	}
	page := codePage{Items: make([]codeResponse, 0, len(items))}
	for _, c := range items {
		page.Items = append(page.Items, h.toResponse(r.Context(), c))
	}
	if next != "" {
		page.NextCursor = &next
	}
	httpapi.WriteJSON(w, http.StatusOK, page)
}

func (h *CodesHandler) patch(w http.ResponseWriter, r *http.Request, userID string) {
	var expected int64
	if im := r.Header.Get("If-Match"); im != "" {
		m := ifMatchRe.FindStringSubmatch(strings.TrimSpace(im))
		if m == nil {
			httpapi.WriteError(w, httpapi.CodeInvalidRequest, `If-Match must be the code's version as a quoted entity-tag, e.g. "7".`, map[string]any{"field": "If-Match"})
			return
		}
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || n < 1 {
			httpapi.WriteError(w, httpapi.CodeInvalidRequest, "If-Match must be a positive version number.", map[string]any{"field": "If-Match"})
			return
		}
		expected = n
	}
	var req updateDestinationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Destination) == "" {
		httpapi.WriteError(w, httpapi.CodeInvalidRequest, "A destination is required.", map[string]any{"field": "destination"})
		return
	}
	c, err := h.svc.UpdateDestination(r.Context(), r.PathValue("id"), userID, req.Destination, expected)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, h.toResponse(r.Context(), c))
}

func (h *CodesHandler) delete(w http.ResponseWriter, r *http.Request, userID string) {
	if err := h.svc.Delete(r.Context(), r.PathValue("id"), userID); err != nil {
		writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CodesHandler) setState(w http.ResponseWriter, r *http.Request, userID string, state domain.CodeState) {
	c, err := h.svc.SetState(r.Context(), r.PathValue("id"), userID, state)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, h.toResponse(r.Context(), c))
}
