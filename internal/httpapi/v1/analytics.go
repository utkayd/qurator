package v1

import (
	"errors"
	"net/http"
	"time"

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi"
	"github.com/utkayd/qurator/internal/store"
)

// UserIDFunc extracts the authenticated user's ID from a request. The auth stream
// supplies the real one (reading the identity the auth middleware attached to the
// context); tests supply a header-driven stub. ok=false means no identity.
type UserIDFunc func(r *http.Request) (userID string, ok bool)

const (
	defaultAnalyticsRange = 30 * 24 * time.Hour
	maxAnalyticsRange     = 366 * 24 * time.Hour
)

// AnalyticsHandler serves GET /v1/codes/{id}/analytics. It reads rollups only — never
// raw scan events — so the answer is identical before and after retention pruning
// (FR-024), and nothing in it can carry a scanner address or a location (FR-022,
// FR-025): the store has no such columns and the response has no such fields.
type AnalyticsHandler struct {
	store  store.Store
	userID UserIDFunc
	// Now is the clock for the default range; tests pin it.
	Now func() time.Time
}

// NewAnalyticsHandler constructs the handler.
func NewAnalyticsHandler(st store.Store, userID UserIDFunc) *AnalyticsHandler {
	return &AnalyticsHandler{store: st, userID: userID, Now: time.Now}
}

// codeAnalytics is the CodeAnalytics schema from contracts/openapi.yaml.
type codeAnalytics struct {
	CodeID     string                      `json:"code_id"`
	From       time.Time                   `json:"from"`
	To         time.Time                   `json:"to"`
	Total      int64                       `json:"total"`
	Series     []seriesPoint               `json:"series"`
	Breakdowns map[string][]breakdownEntry `json:"breakdowns"`
}

type seriesPoint struct {
	BucketStart time.Time `json:"bucket_start"`
	Count       int64     `json:"count"`
}

type breakdownEntry struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// ServeHTTP implements http.Handler.
func (h *AnalyticsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.userID(r)
	if !ok || userID == "" {
		httpapi.WriteError(w, httpapi.CodeUnauthorized, "Authentication is required.", nil)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httpapi.WriteError(w, httpapi.CodeNotFound, "No such code.", nil)
		return
	}

	q := r.URL.Query()
	now := h.Now().UTC()
	to := now
	from := now.Add(-defaultAnalyticsRange)
	var err error
	if s := q.Get("to"); s != "" {
		if to, err = time.Parse(time.RFC3339, s); err != nil {
			httpapi.WriteError(w, httpapi.CodeInvalidRequest, "Query parameter 'to' must be an RFC 3339 timestamp.", map[string]any{"param": "to"})
			return
		}
		if q.Get("from") == "" {
			from = to.Add(-defaultAnalyticsRange)
		}
	}
	if s := q.Get("from"); s != "" {
		if from, err = time.Parse(time.RFC3339, s); err != nil {
			httpapi.WriteError(w, httpapi.CodeInvalidRequest, "Query parameter 'from' must be an RFC 3339 timestamp.", map[string]any{"param": "from"})
			return
		}
	}
	from, to = from.UTC(), to.UTC()
	if !from.Before(to) {
		httpapi.WriteError(w, httpapi.CodeInvalidRequest, "'from' must be earlier than 'to'.", map[string]any{"param": "from"})
		return
	}
	if to.Sub(from) > maxAnalyticsRange {
		httpapi.WriteError(w, httpapi.CodeInvalidRequest, "The requested range may span at most 366 days.", map[string]any{"param": "to", "max_days": 366})
		return
	}
	bucket := domain.BucketDay
	switch b := q.Get("bucket"); b {
	case "", string(domain.BucketDay):
	case string(domain.BucketHour), string(domain.BucketWeek):
		bucket = domain.Bucket(b)
	default:
		httpapi.WriteError(w, httpapi.CodeInvalidRequest, "Query parameter 'bucket' must be one of hour, day, week.", map[string]any{"param": "bucket"})
		return
	}

	ctx := r.Context()
	// Ownership first: a non-owner gets the same 404 as a missing code, never a hint.
	code, err := h.store.GetCodeByID(ctx, id, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpapi.WriteError(w, httpapi.CodeNotFound, "No such code.", nil)
			return
		}
		httpapi.Internal(w, r, err)
		return
	}

	res, err := h.store.QueryAnalytics(ctx, domain.AnalyticsQuery{CodeID: code.ID, From: from, To: to, Bucket: bucket})
	if err != nil {
		httpapi.Internal(w, r, err)
		return
	}

	out := codeAnalytics{
		CodeID: code.ID,
		From:   from,
		To:     to,
		Total:  res.Total,
		Series: make([]seriesPoint, 0, len(res.Series)),
		Breakdowns: map[string][]breakdownEntry{
			string(domain.DimUAFamily):       {},
			string(domain.DimDeviceCategory): {},
			string(domain.DimReferrerHost):   {},
			string(domain.DimIsBot):          {},
		},
	}
	for _, p := range res.Series {
		out.Series = append(out.Series, seriesPoint{BucketStart: p.Start.UTC(), Count: p.Count})
	}
	for dim, rows := range res.Breakdowns {
		if dim == domain.DimTotal {
			continue
		}
		entries := make([]breakdownEntry, 0, len(rows))
		for _, row := range rows {
			entries = append(entries, breakdownEntry{Value: row.Value, Count: row.Count})
		}
		out.Breakdowns[string(dim)] = entries
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusOK, out)
}
