package codes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/utkayd/qurator/internal/blob"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/shortcode"
	"github.com/utkayd/qurator/internal/store"
)

// Renderer produces the persisted PNG for a dynamic code. The QR content is always the
// instance's scan URL for the short code (FR-007), never the destination. Stream A's
// internal/qr provides the real implementation; this package depends only on the
// interface so it can be tested with a fake.
//
// logo is the optional centre overlay's original bytes (PNG or JPEG) and autoRaise
// whether the renderer may raise the EC level to fit it (FR-027). effective is the level
// actually encoded; it equals s.ECLevel unless a logo forced a raise. Renderer errors
// are returned to the caller unwrapped so the HTTP layer can map the typed qr errors.
type Renderer interface {
	Render(ctx context.Context, content string, s domain.Styling, logo []byte, autoRaise bool) (png []byte, effective domain.ECLevel, err error)
}

// Config is the slice of instance configuration the service needs.
type Config struct {
	// BaseURL is the externally visible origin (server.base_url). Scan and image URLs are
	// built on it, and the self-reference check compares destinations against its host.
	BaseURL string
	// AllowedSchemes is the destination scheme allow-list (codes.allowed_schemes).
	AllowedSchemes []string
}

// Sentinel errors for validation failures. Each corresponds to a stable API error code
// (contracts/errors.md); the HTTP layer maps them.
var (
	ErrUnsupportedScheme  = errors.New("codes: unsupported destination scheme")
	ErrSelfReferential    = errors.New("codes: destination points at this instance's scan path")
	ErrInvalidDestination = errors.New("codes: invalid destination")
	ErrAliasInvalid       = errors.New("codes: invalid alias")
	ErrAliasReserved      = errors.New("codes: alias is reserved")
	ErrInvalidStyling     = errors.New("codes: invalid styling")
)

// ValidationError carries the sentinel plus structured details for the error envelope.
type ValidationError struct {
	Err     error
	Details map[string]any
}

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

func vErr(err error, details map[string]any) error {
	return &ValidationError{Err: err, Details: details}
}

// generateAttempts bounds insert-and-retry on a short-code collision (FR-013). With
// 60 bits of entropy a second collision is astronomically unlikely; five attempts is
// generous and keeps a broken RNG from looping forever.
const generateAttempts = 5

// Service implements dynamic-code use cases over the Store and BlobStore contracts.
type Service struct {
	store    store.Store
	blob     blob.BlobStore
	renderer Renderer
	cache    *Cache
	base     *url.URL
	baseRaw  string
	schemes  map[string]bool
	now      func() time.Time
}

// NewService wires the service. cache may be nil for callers that never resolve.
func NewService(st store.Store, bl blob.BlobStore, r Renderer, cache *Cache, cfg Config) *Service {
	schemes := map[string]bool{}
	for _, s := range cfg.AllowedSchemes {
		schemes[strings.ToLower(strings.TrimSpace(s))] = true
	}
	if len(schemes) == 0 {
		schemes["http"], schemes["https"] = true, true
	}
	if cache == nil {
		cache = NewCache()
	}
	baseRaw := strings.TrimRight(cfg.BaseURL, "/")
	var base *url.URL
	if baseRaw != "" {
		if u, err := url.Parse(baseRaw); err == nil {
			base = u
		}
	}
	return &Service{store: st, blob: bl, renderer: r, cache: cache, base: base, baseRaw: baseRaw, schemes: schemes, now: func() time.Time { return time.Now().UTC() }}
}

// AllowedSchemes lists the configured scheme allow-list, sorted for stable error details.
func (s *Service) AllowedSchemes() []string {
	out := make([]string, 0, len(s.schemes))
	for k := range s.schemes {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// ScanURL is the address encoded into the QR symbol.
func (s *Service) ScanURL(shortCode string) string { return s.baseRaw + "/r/" + shortCode }

// ImageURL is where the persisted image is served.
func (s *Service) ImageURL(id string) string { return s.baseRaw + "/i/" + id + ".png" }

// BlobKeyFor is the blob key of a code's persisted PNG, sharded on the id's random part
// so a flat prefix never accumulates one enormous listing.
func BlobKeyFor(id string) string {
	rnd := id
	if i := strings.IndexByte(id, '_'); i >= 0 {
		rnd = id[i+1:]
	}
	for len(rnd) < 4 {
		rnd += "0"
	}
	return "codes/" + rnd[0:2] + "/" + rnd[2:4] + "/" + id + ".png"
}

// LogoBlobKeyFor is the blob key of a code's original logo bytes, sharded like the PNG.
// It has no extension because the logo may be PNG or JPEG; the stored content type
// records which.
func LogoBlobKeyFor(id string) string {
	rnd := id
	if i := strings.IndexByte(id, '_'); i >= 0 {
		rnd = id[i+1:]
	}
	for len(rnd) < 4 {
		rnd += "0"
	}
	return "logos/" + rnd[0:2] + "/" + rnd[2:4] + "/" + id
}

// ---- destination validation (FR-011, FR-012) -----------------------------------------

// ValidateDestination applies the self-reference check first and the scheme allow-list
// second, so a scheme-relative `//host/r/x` is reported for what it is.
func (s *Service) ValidateDestination(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return vErr(ErrInvalidDestination, map[string]any{"field": "destination"})
	}
	u, err := url.Parse(raw)
	if err != nil {
		return vErr(ErrInvalidDestination, map[string]any{"field": "destination"})
	}
	if s.isSelfReferential(u) {
		return vErr(ErrSelfReferential, nil)
	}
	scheme := strings.ToLower(u.Scheme)
	if !s.schemes[scheme] {
		return vErr(ErrUnsupportedScheme, map[string]any{"scheme": scheme, "allowed": s.AllowedSchemes()})
	}
	if u.Host == "" {
		return vErr(ErrInvalidDestination, map[string]any{"field": "destination"})
	}
	return nil
}

// isSelfReferential reports whether u resolves to this instance's /r/ path. The host is
// compared after lower-casing and default-port normalisation; the path after
// percent-decoding (url.Parse already decodes into Path) and dot-segment cleaning, so
// `/%72/x`, `/x/../r/x` and `//host/r/x` are all caught. A host-less path is treated as
// relative to this instance, which is the conservative reading.
func (s *Service) isSelfReferential(u *url.URL) bool {
	p := strings.ToLower(path.Clean("/" + u.Path))
	onScanPath := p == "/r" || strings.HasPrefix(p, "/r/")
	if !onScanPath {
		return false
	}
	if u.Host == "" {
		return true
	}
	if s.base == nil {
		return false
	}
	return sameOrigin(u, s.base)
}

// sameOrigin compares host and effective port. A scheme-relative URL (`//host/r/x`)
// inherits the base's scheme, so its default port is the base's.
func sameOrigin(a, b *url.URL) bool {
	if !strings.EqualFold(a.Hostname(), b.Hostname()) {
		return false
	}
	pa := effectivePort(a)
	if a.Scheme == "" && a.Port() == "" {
		pa = effectivePort(b)
	}
	return pa == effectivePort(b)
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "":
		return "80"
	case "https":
		return "443"
	}
	return ""
}

// ---- styling defaults ----------------------------------------------------------------

// DefaultStyling is applied field-by-field to whatever the caller omitted.
func DefaultStyling() domain.Styling {
	return domain.Styling{
		FgColor:       "#000000",
		BgColor:       "#FFFFFF",
		ModuleShape:   domain.ShapeSquare,
		MarginModules: 4,
		SizePx:        512,
		ECLevel:       domain.ECMedium,
	}
}

// DefaultLogoScale applies when a logo is sent without a scale; it matches the
// ephemeral endpoint's default so the two paths render identically.
const DefaultLogoScale = 0.15

func fillStyling(in domain.Styling) domain.Styling {
	d := DefaultStyling()
	if in.FgColor == "" {
		in.FgColor = d.FgColor
	}
	if in.BgColor == "" {
		in.BgColor = d.BgColor
	}
	if in.ModuleShape == "" {
		in.ModuleShape = d.ModuleShape
	}
	if in.MarginModules == 0 {
		in.MarginModules = d.MarginModules
	}
	if in.SizePx == 0 {
		in.SizePx = d.SizePx
	}
	if in.ECLevel == "" {
		in.ECLevel = d.ECLevel
	}
	if in.ECLevelEffective == "" {
		in.ECLevelEffective = in.ECLevel
	}
	return in
}

// ---- use cases -----------------------------------------------------------------------

// CreateInput is the validated-shape request for Create.
type CreateInput struct {
	UserID      string
	Destination string
	Alias       string // empty = generate
	Styling     domain.Styling
	// Logo is the optional centre overlay (original PNG or JPEG bytes). Styling.LogoScale
	// is its requested scale; a zero scale lets the renderer's default apply.
	Logo []byte
	// LogoAutoRaise lets the renderer raise the EC level when the logo exceeds the
	// requested level's budget (FR-027).
	LogoAutoRaise bool
}

// Create validates, chooses the short code, renders and persists the image, then inserts
// the code (which reserves the short code atomically). Generated codes retry on
// collision; aliases never do — a taken alias is the caller's problem (FR-018).
func (s *Service) Create(ctx context.Context, in CreateInput) (*domain.Code, error) {
	if in.UserID == "" {
		return nil, errors.New("codes: user id is required")
	}
	if err := s.ValidateDestination(in.Destination); err != nil {
		return nil, err
	}
	styling := fillStyling(in.Styling)
	// The blob key is assigned here, never taken from the caller.
	styling.LogoBlobKey = ""
	if len(in.Logo) == 0 {
		styling.LogoScale = 0
	} else if styling.LogoScale == 0 {
		styling.LogoScale = DefaultLogoScale
	}

	isAlias := in.Alias != ""
	var shortCode string
	if isAlias {
		shortCode = shortcode.NormalizeAlias(in.Alias)
		if err := shortcode.ValidateAlias(shortCode); err != nil {
			if errors.Is(err, shortcode.ErrAliasReserved) {
				return nil, vErr(ErrAliasReserved, map[string]any{"alias": shortCode})
			}
			return nil, vErr(ErrAliasInvalid, map[string]any{"alias": shortCode, "reason": reason(err)})
		}
	}

	id := domain.NewCodeID()
	blobKey := BlobKeyFor(id)
	logoKey := ""
	if len(in.Logo) > 0 {
		logoKey = LogoBlobKeyFor(id)
	}
	attempts := 1
	if !isAlias {
		attempts = generateAttempts
	}
	cleanup := func() {
		for _, k := range []string{blobKey, logoKey} {
			if k == "" {
				continue
			}
			if delErr := s.blob.Delete(ctx, k); delErr != nil && !errors.Is(delErr, blob.ErrBlobNotFound) {
				slog.WarnContext(ctx, "codes: orphaned blob after failed create", "key", k, "err", delErr)
			}
		}
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if !isAlias {
			shortCode = shortcode.Generate()
		}
		// Renderer errors propagate unwrapped: the typed qr errors (logo_too_large,
		// contrast_too_low, ...) are part of the API contract and errors.As must reach them.
		png, effective, err := s.renderer.Render(ctx, s.ScanURL(shortCode), styling, in.Logo, in.LogoAutoRaise)
		if err != nil {
			cleanup()
			return nil, err
		}
		if effective != "" {
			styling.ECLevelEffective = effective
		}
		if logoKey != "" && styling.LogoBlobKey == "" {
			ct := http.DetectContentType(in.Logo)
			if _, err := s.blob.Put(ctx, logoKey, bytes.NewReader(in.Logo), int64(len(in.Logo)), ct); err != nil {
				cleanup()
				return nil, fmt.Errorf("codes: store logo: %w", err)
			}
			styling.LogoBlobKey = logoKey
		}
		etag, err := s.blob.Put(ctx, blobKey, bytes.NewReader(png), int64(len(png)), "image/png")
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("codes: store image: %w", err)
		}
		now := s.now()
		c := &domain.Code{
			ID:          id,
			ShortCode:   shortCode,
			IsAlias:     isAlias,
			UserID:      in.UserID,
			Destination: strings.TrimSpace(in.Destination),
			State:       domain.CodeActive,
			Styling:     styling,
			BlobKey:     blobKey,
			BlobETag:    etag,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		err = s.store.CreateCode(ctx, c)
		if err == nil {
			s.cache.Invalidate(shortCode)
			return c, nil
		}
		lastErr = err
		if !errors.Is(err, store.ErrAliasTaken) {
			break
		}
		if isAlias {
			lastErr = vErr(store.ErrAliasTaken, map[string]any{"alias": shortCode})
			break
		}
		slog.WarnContext(ctx, "codes: generated short code collided; retrying", "attempt", i+1)
	}
	cleanup()
	return nil, lastErr
}

func reason(err error) string {
	switch {
	case errors.Is(err, shortcode.ErrAliasTooShort):
		return "too_short"
	case errors.Is(err, shortcode.ErrAliasTooLong):
		return "too_long"
	case errors.Is(err, shortcode.ErrAliasBadChar):
		return "bad_character"
	case errors.Is(err, shortcode.ErrAliasBadEdge):
		return "bad_edge"
	case errors.Is(err, shortcode.ErrAliasDoubleHyphen):
		return "double_hyphen"
	case errors.Is(err, shortcode.ErrAliasLooksGenerated):
		return "looks_generated"
	default:
		return "invalid"
	}
}

// Get returns the caller's code, deleted rows included (the API shows them by id).
func (s *Service) Get(ctx context.Context, id, userID string) (*domain.Code, error) {
	return s.store.GetCodeByID(ctx, id, userID)
}

// List pages the caller's live codes.
func (s *Service) List(ctx context.Context, f domain.CodeFilter) ([]*domain.Code, string, error) {
	return s.store.ListCodes(ctx, f)
}

// ConflictError reports a lost optimistic-concurrency race with the version that won.
type ConflictError struct {
	Expected int64
	Actual   int64
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("codes: version conflict: expected %d, actual %d", e.Expected, e.Actual)
}

func (e *ConflictError) Unwrap() error { return store.ErrConflict }

// lastWriteWinsAttempts bounds the read-then-update loop used when no If-Match is sent.
const lastWriteWinsAttempts = 3

// UpdateDestination re-validates the destination (FR-011 on every update) and applies
// it with optimistic concurrency. expectedVersion <= 0 means the caller accepts
// last-write-wins: the current version is read and used, retrying a few times if
// another writer slips in between.
func (s *Service) UpdateDestination(ctx context.Context, id, userID, dest string, expectedVersion int64) (*domain.Code, error) {
	if err := s.ValidateDestination(dest); err != nil {
		return nil, err
	}
	dest = strings.TrimSpace(dest)
	attempts := 1
	if expectedVersion <= 0 {
		attempts = lastWriteWinsAttempts
	}
	for i := 0; i < attempts; i++ {
		cur, err := s.store.GetCodeByID(ctx, id, userID)
		if err != nil {
			return nil, err
		}
		v := expectedVersion
		if v <= 0 {
			v = cur.Version
		}
		err = s.store.UpdateDestination(ctx, id, userID, dest, v)
		if err == nil {
			s.cache.Invalidate(cur.ShortCode)
			return s.store.GetCodeByID(ctx, id, userID)
		}
		if !errors.Is(err, store.ErrConflict) {
			return nil, err
		}
		if expectedVersion > 0 || cur.State == domain.CodeDeleted {
			latest, gerr := s.store.GetCodeByID(ctx, id, userID)
			if gerr != nil {
				return nil, gerr
			}
			return nil, &ConflictError{Expected: v, Actual: latest.Version}
		}
	}
	return nil, fmt.Errorf("codes: update kept losing the race: %w", store.ErrConflict)
}

// SetState enables or disables a code. Deleted is terminal: the store reports
// ErrConflict, which the API surfaces as 409.
func (s *Service) SetState(ctx context.Context, id, userID string, state domain.CodeState) (*domain.Code, error) {
	if err := s.store.SetCodeState(ctx, id, userID, state); err != nil {
		return nil, err
	}
	c, err := s.store.GetCodeByID(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	s.cache.Invalidate(c.ShortCode)
	return c, nil
}

// Delete soft-deletes. The short code stays reserved (FR-018) and the image stays in
// the blob store so an already-printed artefact still resolves to the landing page.
func (s *Service) Delete(ctx context.Context, id, userID string) error {
	c, err := s.store.GetCodeByID(ctx, id, userID)
	if err != nil {
		return err
	}
	if err := s.store.DeleteCode(ctx, id, userID); err != nil {
		return err
	}
	s.cache.Invalidate(c.ShortCode)
	return nil
}

// ---- scan path -----------------------------------------------------------------------

// Resolver is what the public redirect handler depends on.
type Resolver interface {
	Resolve(ctx context.Context, shortCode string) (Resolution, error)
}

// Resolve answers a scan with at most ONE store lookup on a cold key and none on a warm
// one (FR-017). Unknown codes are cached as negatives for the same TTL.
func (s *Service) Resolve(ctx context.Context, shortCode string) (Resolution, error) {
	if r, ok := s.cache.Get(shortCode); ok {
		return r, nil
	}
	c, err := s.store.GetCodeByShortCode(ctx, shortCode)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			r := Resolution{Found: false}
			s.cache.Set(shortCode, r)
			return r, nil
		}
		return Resolution{}, err
	}
	r := Resolution{CodeID: c.ID, Destination: c.Destination, State: c.State, Found: true}
	s.cache.Set(c.ShortCode, r)
	return r, nil
}
