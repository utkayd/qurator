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
	"sync"
	"time"

	"github.com/utkayd/qurator/internal/blob"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/qr"
	"github.com/utkayd/qurator/internal/shortcode"
	"github.com/utkayd/qurator/internal/store"
)

// Renderer produces the persisted PNG for a code. For a dynamic code the QR content is
// the instance's scan URL for the short code (FR-007), never the destination; for a
// direct code it is the destination itself (spec 002, FR-102). Stream A's internal/qr
// provides the real implementation; this package depends only on the interface so it
// can be tested with a fake.
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

	// URLMode is images.url_mode (spec 003, FR-201): "instance" (default, also for the
	// empty string), "public" or "presigned". Config validation guarantees the latter
	// two only arrive with a blob store that implements blob.URLer.
	URLMode string
	// PublicBaseURL is images.public_base_url, already normalised (no trailing slash).
	PublicBaseURL string
	// PresignTTL is images.presign_ttl; zero means one hour.
	PresignTTL time.Duration
	// BatchMax is codes.batch_max; zero means DefaultBatchMax.
	BatchMax int
	// BatchWorkers is codes.batch_workers; zero means DefaultBatchWorkers.
	BatchWorkers int
}

// URL modes (images.url_mode).
const (
	URLModeInstance  = "instance"
	URLModePublic    = "public"
	URLModePresigned = "presigned"
)

// Batch defaults, mirrored from config's defaults so the service is usable without it.
const (
	DefaultBatchMax     = 500
	DefaultBatchWorkers = 4
	DefaultPresignTTL   = time.Hour
	// MaxClientRefLen bounds client_ref (spec 003 assumptions: opaque, <= 128 chars).
	MaxClientRefLen = 128
)

// Sentinel errors for validation failures. Each corresponds to a stable API error code
// (contracts/errors.md); the HTTP layer maps them.
var (
	ErrUnsupportedScheme  = errors.New("codes: unsupported destination scheme")
	ErrSelfReferential    = errors.New("codes: destination points at this instance's scan path")
	ErrInvalidDestination = errors.New("codes: invalid destination")
	ErrAliasInvalid       = errors.New("codes: invalid alias")
	ErrAliasReserved      = errors.New("codes: alias is reserved")
	ErrInvalidStyling     = errors.New("codes: invalid styling")
	ErrInvalidMode        = errors.New("codes: invalid mode")
	// ErrDirectImmutable refuses destination and state changes on a direct code: the
	// destination is printed into the image, and disable/enable only mean something on
	// the redirect path (spec 002, FR-104).
	ErrDirectImmutable = errors.New("codes: direct code is immutable")
	// ErrClientRefInvalid: a client_ref is too long or repeated within one batch (spec
	// 003). The ValidationError details say which.
	ErrClientRefInvalid = errors.New("codes: invalid client_ref")
)

// ClientRefConflictError: the caller reused a client_ref with a different destination or
// mode (spec 003, FR-206). It names the code that holds the ref so the caller can decide;
// it is never silently ignored.
type ClientRefConflictError struct {
	ClientRef  string
	ExistingID string
}

func (e *ClientRefConflictError) Error() string {
	return fmt.Sprintf("codes: client_ref %q already used by code %s with a different destination or mode", e.ClientRef, e.ExistingID)
}

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
	urler    blob.URLer // nil when the blob store cannot address its objects
	renderer Renderer
	cache    *Cache
	base     *url.URL
	baseRaw  string
	schemes  map[string]bool
	now      func() time.Time

	urlMode      string
	publicBase   string
	presignTTL   time.Duration
	batchMax     int
	batchWorkers int
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
	svc := &Service{
		store: st, blob: bl, renderer: r, cache: cache, base: base, baseRaw: baseRaw, schemes: schemes,
		now:        func() time.Time { return time.Now().UTC() },
		urlMode:    cfg.URLMode,
		publicBase: strings.TrimRight(cfg.PublicBaseURL, "/"),
		presignTTL: cfg.PresignTTL, batchMax: cfg.BatchMax, batchWorkers: cfg.BatchWorkers,
	}
	if svc.urlMode == "" {
		svc.urlMode = URLModeInstance
	}
	if svc.presignTTL <= 0 {
		svc.presignTTL = DefaultPresignTTL
	}
	if svc.batchMax <= 0 {
		svc.batchMax = DefaultBatchMax
	}
	if svc.batchWorkers <= 0 {
		svc.batchWorkers = DefaultBatchWorkers
	}
	if u, ok := bl.(blob.URLer); ok {
		svc.urler = u
	}
	return svc
}

// BatchMax is the largest batch CreateBatch accepts (codes.batch_max).
func (s *Service) BatchMax() int { return s.batchMax }

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

// InstanceImageURL is this instance's own address for a code's image (GET /i/{id}.png).
func (s *Service) InstanceImageURL(id string) string { return s.baseRaw + "/i/" + id + ".png" }

// ImageURL is where a code's persisted image should be fetched from, per images.url_mode
// (spec 003, FR-202): the instance route, the public bucket address, or a presigned link.
// If the blob store cannot produce the configured kind of URL (which config validation
// rules out, so this is a defence) the instance route is returned and a warning logged.
func (s *Service) ImageURL(ctx context.Context, c *domain.Code) string {
	switch s.urlMode {
	case URLModePublic:
		if u, ok := s.publicURL(c); ok {
			return u
		}
	case URLModePresigned:
		if u, ok := s.presignedURL(ctx, c); ok {
			return u
		}
	}
	return s.InstanceImageURL(c.ID)
}

// StorageURL is the image's address in the blob store, independent of this instance:
// the public URL when a base is configured, else a presigned link. ok is false when the
// blob store cannot derive one (the filesystem driver), in which case callers omit the
// field rather than fabricate it (FR-202).
func (s *Service) StorageURL(ctx context.Context, c *domain.Code) (string, bool) {
	if s.urler == nil || c.BlobKey == "" {
		return "", false
	}
	if s.publicBase != "" {
		return s.publicURL(c)
	}
	return s.presignedURL(ctx, c)
}

func (s *Service) publicURL(c *domain.Code) (string, bool) {
	if s.urler == nil || s.publicBase == "" || c.BlobKey == "" {
		return "", false
	}
	u, err := s.urler.PublicURL(c.BlobKey, s.publicBase)
	if err != nil {
		slog.Warn("codes: public image URL", "code", c.ID, "err", err)
		return "", false
	}
	return u, true
}

func (s *Service) presignedURL(ctx context.Context, c *domain.Code) (string, bool) {
	if s.urler == nil || c.BlobKey == "" {
		return "", false
	}
	u, err := s.urler.PresignedURL(ctx, c.BlobKey, s.presignTTL)
	if err != nil {
		slog.WarnContext(ctx, "codes: presigned image URL", "code", c.ID, "err", err)
		return "", false
	}
	return u, true
}

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
	UserID string
	// Mode selects what the image encodes. Empty means domain.ModeDynamic; any value
	// other than the two domain constants is ErrInvalidMode.
	Mode        domain.CodeMode
	Destination string
	Alias       string // empty = generate
	Styling     domain.Styling
	// Logo is the optional centre overlay (original PNG or JPEG bytes). Styling.LogoScale
	// is its requested scale; a zero scale lets the renderer's default apply.
	Logo []byte
	// LogoAutoRaise lets the renderer raise the EC level when the logo exceeds the
	// requested level's budget (FR-027).
	LogoAutoRaise bool
	// ClientRef is the caller's optional idempotency key (spec 003, FR-206). Create
	// stores it; CreateBatch also honours it (an existing code with the same
	// destination and mode is returned instead of a new one).
	ClientRef string
}

// prepared is a CreateInput after validation, with every default applied: what the
// render and the row need, independent of which short code ends up chosen.
type prepared struct {
	mode        domain.CodeMode
	destination string
	styling     domain.Styling
	isAlias     bool
	shortCode   string // the normalised alias; empty when one is to be generated
	logo        []byte
	autoRaise   bool
	clientRef   string
}

// prepare runs every validation Create and CreateBatch share (mode, destination, styling
// defaults, direct-code capacity, alias shape, client_ref length) without touching the
// store or the blob store. Errors are the same typed values Create has always returned.
func (s *Service) prepare(in CreateInput) (*prepared, error) {
	if in.UserID == "" {
		return nil, errors.New("codes: user id is required")
	}
	mode := in.Mode
	switch mode {
	case "":
		mode = domain.ModeDynamic
	case domain.ModeDynamic, domain.ModeDirect:
	default:
		return nil, vErr(ErrInvalidMode, map[string]any{"field": "mode"})
	}
	if len(in.ClientRef) > MaxClientRefLen {
		return nil, vErr(ErrClientRefInvalid, map[string]any{"field": "client_ref", "reason": "too_long", "max_length": MaxClientRefLen})
	}
	if err := s.ValidateDestination(in.Destination); err != nil {
		return nil, err
	}
	p := &prepared{mode: mode, destination: strings.TrimSpace(in.Destination), logo: in.Logo, autoRaise: in.LogoAutoRaise, clientRef: in.ClientRef}
	p.styling = fillStyling(in.Styling)
	// The blob key is assigned at materialisation, never taken from the caller.
	p.styling.LogoBlobKey = ""
	if len(in.Logo) == 0 {
		p.styling.LogoScale = 0
	} else if p.styling.LogoScale == 0 {
		p.styling.LogoScale = DefaultLogoScale
	}
	if mode == domain.ModeDirect {
		// A direct code encodes the whole destination, so it must fit the symbol
		// (FR-103). This is the cheap necessary check at the requested level, made before
		// any short code or blob work; the renderer repeats it against the level it
		// actually encodes (a logo may raise it, which only lowers the cap) and its typed
		// error propagates unwrapped, exactly as for ephemeral generation.
		if limit := qr.Capacity(p.styling.ECLevel); len(p.destination) > limit {
			return nil, &qr.ContentTooLargeError{Limit: limit, Actual: len(p.destination), Level: p.styling.ECLevel}
		}
	}
	if in.Alias != "" {
		p.isAlias = true
		p.shortCode = shortcode.NormalizeAlias(in.Alias)
		if err := shortcode.ValidateAlias(p.shortCode); err != nil {
			if errors.Is(err, shortcode.ErrAliasReserved) {
				return nil, vErr(ErrAliasReserved, map[string]any{"alias": p.shortCode})
			}
			return nil, vErr(ErrAliasInvalid, map[string]any{"alias": p.shortCode, "reason": reason(err)})
		}
	}
	return p, nil
}

// materialise renders the image for one (id, shortCode) pair, stores the logo and the
// PNG, and returns the row to insert. On any failure every blob it wrote is removed and
// the error is returned unwrapped (renderer errors are typed API errors).
func (s *Service) materialise(ctx context.Context, userID, id, shortCode string, p *prepared) (*domain.Code, error) {
	blobKey := BlobKeyFor(id)
	content := s.ScanURL(shortCode)
	if p.mode == domain.ModeDirect {
		content = p.destination
	}
	styling := p.styling
	png, effective, err := s.renderer.Render(ctx, content, styling, p.logo, p.autoRaise)
	if err != nil {
		return nil, err
	}
	if effective != "" {
		styling.ECLevelEffective = effective
	}
	var written []string
	fail := func(err error) (*domain.Code, error) {
		s.removeBlobs(ctx, written)
		return nil, err
	}
	if len(p.logo) > 0 {
		logoKey := LogoBlobKeyFor(id)
		ct := http.DetectContentType(p.logo)
		if _, err := s.blob.Put(ctx, logoKey, bytes.NewReader(p.logo), int64(len(p.logo)), ct); err != nil {
			return fail(fmt.Errorf("codes: store logo: %w", err))
		}
		written = append(written, logoKey)
		styling.LogoBlobKey = logoKey
	}
	etag, err := s.blob.Put(ctx, blobKey, bytes.NewReader(png), int64(len(png)), "image/png")
	if err != nil {
		return fail(fmt.Errorf("codes: store image: %w", err))
	}
	now := s.now()
	return &domain.Code{
		ID:          id,
		ShortCode:   shortCode,
		IsAlias:     p.isAlias,
		UserID:      userID,
		Mode:        p.mode,
		ClientRef:   p.clientRef,
		Destination: p.destination,
		State:       domain.CodeActive,
		Styling:     styling,
		BlobKey:     blobKey,
		BlobETag:    etag,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// removeBlobs deletes the blobs of a code whose row never landed. A failure here only
// orphans an object; it is logged, never returned.
func (s *Service) removeBlobs(ctx context.Context, keys []string) {
	for _, k := range keys {
		if k == "" {
			continue
		}
		if delErr := s.blob.Delete(ctx, k); delErr != nil && !errors.Is(delErr, blob.ErrBlobNotFound) {
			slog.WarnContext(ctx, "codes: orphaned blob after failed create", "key", k, "err", delErr)
		}
	}
}

func blobKeysOf(c *domain.Code) []string {
	return []string{c.BlobKey, c.Styling.LogoBlobKey}
}

// Create validates, chooses the short code, renders and persists the image, then inserts
// the code (which reserves the short code atomically). Generated codes retry on
// collision; aliases never do — a taken alias is the caller's problem (FR-018).
func (s *Service) Create(ctx context.Context, in CreateInput) (*domain.Code, error) {
	p, err := s.prepare(in)
	if err != nil {
		return nil, err
	}
	attempts := 1
	if !p.isAlias {
		attempts = generateAttempts
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		shortCode := p.shortCode
		if !p.isAlias {
			shortCode = shortcode.Generate()
		}
		c, err := s.materialise(ctx, in.UserID, domain.NewCodeID(), shortCode, p)
		if err != nil {
			return nil, err
		}
		err = s.store.CreateCode(ctx, c)
		if err == nil {
			s.cache.Invalidate(shortCode)
			return c, nil
		}
		s.removeBlobs(ctx, blobKeysOf(c))
		lastErr = err
		if !errors.Is(err, store.ErrAliasTaken) {
			break
		}
		if p.isAlias {
			lastErr = vErr(store.ErrAliasTaken, map[string]any{"alias": shortCode})
			break
		}
		slog.WarnContext(ctx, "codes: generated short code collided; retrying", "attempt", i+1)
	}
	return nil, lastErr
}

// ---- batch creation (spec 003) --------------------------------------------------------

// BatchStatus is the per-item outcome of CreateBatch.
type BatchStatus string

// Per-item outcomes (FR-205).
const (
	BatchCreated  BatchStatus = "created"
	BatchExisting BatchStatus = "existing"
	BatchError    BatchStatus = "error"
)

// BatchResult is one item's outcome, at the item's input index. Code is set for created
// and existing; Err for error, carrying the same typed values Create returns.
type BatchResult struct {
	Index  int
	Status BatchStatus
	Code   *domain.Code
	Err    error
}

// CreateBatch creates many codes for one user (FR-205/FR-206/FR-207). It never fails as a
// whole — the caller checks BatchMax first — and returns exactly one result per item, in
// input order. Phases:
//
//  1. client_ref resolution: an item whose ref this user already used comes back as
//     existing (same destination and mode) or as a ClientRefConflictError; a ref repeated
//     within the batch is an error on its second occurrence.
//  2. validation, shared with Create via prepare, plus alias availability (so a taken
//     alias fails its own item instead of the whole transaction).
//  3. rendering and blob writes for the surviving items, in parallel, bounded by
//     codes.batch_workers.
//  4. ONE store transaction for every rendered item. If it fails — a client_ref or alias
//     race lost between the pre-check and the insert, or the store being down — every
//     rendered item is marked error and its blobs are removed, so no partial set is left.
//
// Per-row attribution of a store failure is deliberately not attempted: the drivers can
// only report the first violated constraint, not which row hit it, and the pre-checks
// make such a failure a genuine race rather than the normal path.
func (s *Service) CreateBatch(ctx context.Context, userID string, items []CreateInput) []BatchResult {
	results := make([]BatchResult, len(items))
	for i := range results {
		results[i] = BatchResult{Index: i, Status: BatchError}
	}
	if userID == "" {
		for i := range results {
			results[i].Err = errors.New("codes: user id is required")
		}
		return results
	}

	type pendingItem struct {
		idx int
		p   *prepared
	}
	var pending []pendingItem
	seenRef := map[string]int{}
	seenAlias := map[string]int{}
	for i, in := range items {
		in.UserID = userID
		if ref := in.ClientRef; ref != "" && len(ref) <= MaxClientRefLen {
			if _, dup := seenRef[ref]; dup {
				results[i].Err = vErr(ErrClientRefInvalid, map[string]any{"field": "client_ref", "reason": "duplicate_in_batch", "client_ref": ref})
				continue
			}
			seenRef[ref] = i
			existing, err := s.store.GetCodeByClientRef(ctx, userID, ref)
			switch {
			case err == nil:
				if existing.Destination == strings.TrimSpace(in.Destination) && modeOf(existing) == modeOf(&domain.Code{Mode: in.Mode}) {
					results[i] = BatchResult{Index: i, Status: BatchExisting, Code: existing}
				} else {
					results[i].Err = &ClientRefConflictError{ClientRef: ref, ExistingID: existing.ID}
				}
				continue
			case !errors.Is(err, store.ErrNotFound):
				results[i].Err = err
				continue
			}
		}
		p, err := s.prepare(in)
		if err != nil {
			results[i].Err = err
			continue
		}
		if p.isAlias {
			if _, dup := seenAlias[p.shortCode]; dup {
				results[i].Err = vErr(store.ErrAliasTaken, map[string]any{"alias": p.shortCode})
				continue
			}
			ok, err := s.store.IsAliasAvailable(ctx, p.shortCode)
			if err != nil {
				results[i].Err = err
				continue
			}
			if !ok {
				results[i].Err = vErr(store.ErrAliasTaken, map[string]any{"alias": p.shortCode})
				continue
			}
			seenAlias[p.shortCode] = i
		}
		pending = append(pending, pendingItem{idx: i, p: p})
	}
	if len(pending) == 0 {
		return results
	}

	// Render in parallel: a semaphore bounds concurrency, the wait group joins. Each
	// goroutine writes only its own slots, so no further synchronisation is needed.
	rendered := make([]*domain.Code, len(pending))
	sem := make(chan struct{}, s.batchWorkers)
	var wg sync.WaitGroup
	for j, pe := range pending {
		wg.Add(1)
		sem <- struct{}{}
		go func(j int, pe pendingItem) {
			defer wg.Done()
			defer func() { <-sem }()
			shortCode := pe.p.shortCode
			if !pe.p.isAlias {
				shortCode = shortcode.Generate()
			}
			c, err := s.materialise(ctx, userID, domain.NewCodeID(), shortCode, pe.p)
			if err != nil {
				results[pe.idx].Err = err
				return
			}
			rendered[j] = c
		}(j, pe)
	}
	wg.Wait()

	var toInsert []*domain.Code
	var insertIdx []int
	for j, c := range rendered {
		if c != nil {
			toInsert = append(toInsert, c)
			insertIdx = append(insertIdx, pending[j].idx)
		}
	}
	if len(toInsert) == 0 {
		return results
	}
	if err := s.store.CreateCodes(ctx, toInsert); err != nil {
		// A generated short code or a client_ref lost a race, or the store is down:
		// nothing was written, so remove every image and fail every pending item.
		if errors.Is(err, store.ErrAliasTaken) || errors.Is(err, store.ErrClientRefTaken) {
			err = vErr(err, map[string]any{"reason": "lost_race"})
		}
		for k, c := range toInsert {
			s.removeBlobs(ctx, blobKeysOf(c))
			results[insertIdx[k]].Err = err
		}
		return results
	}
	for k, c := range toInsert {
		results[insertIdx[k]] = BatchResult{Index: insertIdx[k], Status: BatchCreated, Code: c}
		s.cache.Invalidate(c.ShortCode)
	}
	return results
}

// modeOf reads a code's mode with the pre-002 default applied.
func modeOf(c *domain.Code) domain.CodeMode {
	if c.Mode == "" {
		return domain.ModeDynamic
	}
	return c.Mode
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
//
// The code is loaded before anything else: ownership (ErrNotFound) comes first so a
// non-owner learns nothing, then a direct code is refused outright (ErrDirectImmutable,
// FR-104) before the new destination is even validated — the answer is "cannot change"
// regardless of what it would have changed to.
func (s *Service) UpdateDestination(ctx context.Context, id, userID, dest string, expectedVersion int64) (*domain.Code, error) {
	cur, err := s.store.GetCodeByID(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	if cur.Mode == domain.ModeDirect {
		return nil, vErr(ErrDirectImmutable, map[string]any{"mode": string(cur.Mode)})
	}
	if err := s.ValidateDestination(dest); err != nil {
		return nil, err
	}
	dest = strings.TrimSpace(dest)
	attempts := 1
	if expectedVersion <= 0 {
		attempts = lastWriteWinsAttempts
	}
	for i := 0; i < attempts; i++ {
		if i > 0 {
			if cur, err = s.store.GetCodeByID(ctx, id, userID); err != nil {
				return nil, err
			}
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
// ErrConflict, which the API surfaces as 409. A direct code has no redirect path for
// the state to gate, so it is refused with ErrDirectImmutable (FR-104), after the
// ownership check.
func (s *Service) SetState(ctx context.Context, id, userID string, state domain.CodeState) (*domain.Code, error) {
	cur, err := s.store.GetCodeByID(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	if cur.Mode == domain.ModeDirect {
		return nil, vErr(ErrDirectImmutable, map[string]any{"mode": string(cur.Mode)})
	}
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
