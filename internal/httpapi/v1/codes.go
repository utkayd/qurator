package v1

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/utkayd/qurator/internal/codes"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi"
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
	maxBodyBytes     = 64 << 10
	defaultPageLimit = 50
	maxPageLimit     = 200
	maxCursorLen     = 512
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

type stylingRequest struct {
	FgColor       *string          `json:"fg_color"`
	BgColor       *string          `json:"bg_color"`
	ModuleShape   *string          `json:"module_shape"`
	MarginModules *int             `json:"margin_modules"`
	SizePx        *int             `json:"size_px"`
	ECLevel       *string          `json:"ec_level"`
	Logo          *json.RawMessage `json:"logo"`
}

type createCodeRequest struct {
	Destination string          `json:"destination"`
	Alias       string          `json:"alias"`
	Styling     *stylingRequest `json:"styling"`
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

type codeResponse struct {
	ID          string         `json:"id"`
	ShortCode   string         `json:"short_code"`
	Version     int64          `json:"version"`
	IsAlias     bool           `json:"is_alias"`
	Destination string         `json:"destination"`
	State       string         `json:"state"`
	Styling     stylingProfile `json:"styling"`
	ImageURL    string         `json:"image_url"`
	ScanURL     string         `json:"scan_url"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type codePage struct {
	Items      []codeResponse `json:"items"`
	NextCursor *string        `json:"next_cursor"`
}

func (h *CodesHandler) toResponse(c *domain.Code) codeResponse {
	return codeResponse{
		ID:          c.ID,
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
		ImageURL:  h.svc.ImageURL(c.ID),
		ScanURL:   h.svc.ScanURL(c.ShortCode),
		CreatedAt: c.CreatedAt.UTC(),
		UpdatedAt: c.UpdatedAt.UTC(),
	}
}

var hexColorRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// stylingFromRequest validates the StylingRequest schema bounds; the renderer applies
// the deeper checks (contrast, logo area) it owns.
func stylingFromRequest(in *stylingRequest) (domain.Styling, string, bool) {
	var out domain.Styling
	if in == nil {
		return out, "", true
	}
	if in.Logo != nil {
		return out, "styling.logo", false // logos on dynamic codes arrive with the renderer stream
	}
	if in.FgColor != nil {
		if !hexColorRe.MatchString(*in.FgColor) {
			return out, "styling.fg_color", false
		}
		out.FgColor = *in.FgColor
	}
	if in.BgColor != nil {
		if !hexColorRe.MatchString(*in.BgColor) {
			return out, "styling.bg_color", false
		}
		out.BgColor = *in.BgColor
	}
	if in.ModuleShape != nil {
		switch domain.ModuleShape(*in.ModuleShape) {
		case domain.ShapeSquare, domain.ShapeDot, domain.ShapeRounded:
			out.ModuleShape = domain.ModuleShape(*in.ModuleShape)
		default:
			return out, "styling.module_shape", false
		}
	}
	if in.MarginModules != nil {
		if *in.MarginModules < 4 || *in.MarginModules > 64 {
			return out, "styling.margin_modules", false
		}
		out.MarginModules = *in.MarginModules
	}
	if in.SizePx != nil {
		if *in.SizePx < 64 || *in.SizePx > 4096 {
			return out, "styling.size_px", false
		}
		out.SizePx = *in.SizePx
	}
	if in.ECLevel != nil {
		switch domain.ECLevel(*in.ECLevel) {
		case domain.ECLow, domain.ECMedium, domain.ECQuartile, domain.ECHigh:
			out.ECLevel = domain.ECLevel(*in.ECLevel)
		default:
			return out, "styling.ec_level", false
		}
	}
	return out, "", true
}

// writeServiceError maps service and store errors onto the stable catalogue.
func writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *codes.ValidationError
	details := map[string]any(nil)
	if errors.As(err, &ve) {
		details = ve.Details
	}
	var ce *codes.ConflictError
	switch {
	case errors.As(err, &ce):
		httpapi.WriteError(w, httpapi.CodeConflict, "The code was modified by another request; re-read it and retry with its current version.",
			map[string]any{"expected": ce.Expected, "actual": ce.Actual})
	case errors.Is(err, codes.ErrUnsupportedScheme):
		httpapi.WriteError(w, httpapi.CodeUnsupportedScheme, "The destination uses a scheme this instance does not permit.", details)
	case errors.Is(err, codes.ErrSelfReferential):
		httpapi.WriteError(w, httpapi.CodeSelfReferentialDestination, "The destination points back at this instance's scan path.", nil)
	case errors.Is(err, codes.ErrInvalidDestination):
		httpapi.WriteError(w, httpapi.CodeInvalidRequest, "The destination is not a valid absolute URL.", details)
	case errors.Is(err, codes.ErrAliasReserved):
		httpapi.WriteError(w, httpapi.CodeAliasReserved, "That alias is reserved by this instance.", details)
	case errors.Is(err, codes.ErrAliasInvalid):
		httpapi.WriteError(w, httpapi.CodeAliasInvalid, "The alias does not meet the character set, length, or shape rules.", details)
	case errors.Is(err, store.ErrAliasTaken):
		httpapi.WriteError(w, httpapi.CodeAliasTaken, "That alias is already in use or reserved by a deleted code.", details)
	case errors.Is(err, store.ErrNotFound):
		httpapi.WriteError(w, httpapi.CodeNotFound, "No such code.", nil)
	case errors.Is(err, store.ErrConflict):
		httpapi.WriteError(w, httpapi.CodeConflict, "The code is in a state that does not allow this change.", nil)
	default:
		httpapi.Internal(w, r, err)
	}
}

// ---- operations ----------------------------------------------------------------------

func (h *CodesHandler) create(w http.ResponseWriter, r *http.Request, userID string) {
	var req createCodeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Destination) == "" {
		httpapi.WriteError(w, httpapi.CodeInvalidRequest, "A destination is required.", map[string]any{"field": "destination"})
		return
	}
	styling, field, ok := stylingFromRequest(req.Styling)
	if !ok {
		httpapi.WriteError(w, httpapi.CodeInvalidRequest, "A styling parameter is out of range or unsupported.", map[string]any{"field": field})
		return
	}
	c, err := h.svc.Create(r.Context(), codes.CreateInput{UserID: userID, Destination: req.Destination, Alias: req.Alias, Styling: styling})
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/codes/"+c.ID)
	httpapi.WriteJSON(w, http.StatusCreated, h.toResponse(c))
}

func (h *CodesHandler) get(w http.ResponseWriter, r *http.Request, userID string) {
	c, err := h.svc.Get(r.Context(), r.PathValue("id"), userID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, h.toResponse(c))
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
		page.Items = append(page.Items, h.toResponse(c))
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
	httpapi.WriteJSON(w, http.StatusOK, h.toResponse(c))
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
	httpapi.WriteJSON(w, http.StatusOK, h.toResponse(c))
}
