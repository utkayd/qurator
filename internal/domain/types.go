package domain

import "time"

// User is an identity that owns codes and tokens. v1 is single-tenant; the type exists so
// multi-tenancy is an added field, not a redesign.
type User struct {
	ID           string
	Email        string
	PasswordHash string // PHC-encoded Argon2id; empty for forward-auth users
	IsAdmin      bool
	TokenVersion int64 // bumping invalidates every session at once
	Source       UserSource
	CreatedAt    time.Time
	LastLoginAt  *time.Time
}

// UserSource records how an identity is established.
type UserSource string

const (
	UserSourceLocal       UserSource = "local"
	UserSourceForwardAuth UserSource = "forward_auth"
)

// APIToken is a revocable machine credential. The secret exists only at creation; only
// its SHA-256 is stored (research.md §2 explains why not Argon2id).
type APIToken struct {
	ID         string
	UserID     string
	Name       string
	SecretHash []byte
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
	ExpiresAt  *time.Time
}

// Revoked reports whether the token has been revoked.
func (t *APIToken) Revoked() bool { return t.RevokedAt != nil }

// CodeState is the lifecycle state of a dynamic code. Deleted is terminal.
type CodeState string

const (
	CodeActive   CodeState = "active"
	CodeDisabled CodeState = "disabled"
	CodeDeleted  CodeState = "deleted"
)

// Code is a persisted dynamic QR code whose destination may change after printing.
type Code struct {
	ID          string
	ShortCode   string // generated or alias; immutable; unique case-insensitively
	IsAlias     bool
	UserID      string
	Destination string
	State       CodeState
	Styling     Styling
	BlobKey     string
	BlobETag    string
	Version     int64 // optimistic-concurrency counter; sent as If-Match
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
}

// ECLevel is a QR error-correction level.
type ECLevel string

const (
	ECLow      ECLevel = "L"
	ECMedium   ECLevel = "M"
	ECQuartile ECLevel = "Q"
	ECHigh     ECLevel = "H"
)

// ModuleShape selects the rendered geometry of a QR module.
type ModuleShape string

const (
	ShapeSquare  ModuleShape = "square"
	ShapeDot     ModuleShape = "dot"
	ShapeRounded ModuleShape = "rounded"
)

// Styling is the visual configuration of a rendered code. ECLevel is what the user asked
// for; ECLevelEffective is what was encoded after automatic raising for a logo. Both are
// kept so the UI can explain itself (data-model.md).
type Styling struct {
	ID               string
	FgColor          string // #RRGGBB
	BgColor          string // #RRGGBB
	ModuleShape      ModuleShape
	MarginModules    int
	SizePx           int
	ECLevel          ECLevel
	ECLevelEffective ECLevel
	LogoBlobKey      string  // empty when no logo
	LogoScale        float64 // fraction of module area; 0 when no logo
}

// ScanEvent is one recorded scan.
//
// There is deliberately no network-address field and no geographic field on this type.
// FR-022 and FR-025 forbid both; do not add them. Referrer is host-only by construction.
type ScanEvent struct {
	CodeID         string
	OccurredAt     time.Time
	UAFamily       string
	DeviceCategory DeviceCategory
	ReferrerHost   string
	IsBot          bool
}

// DeviceCategory is the coarse device class derived from the user agent.
type DeviceCategory string

const (
	DeviceDesktop DeviceCategory = "desktop"
	DeviceMobile  DeviceCategory = "mobile"
	DeviceTablet  DeviceCategory = "tablet"
	DeviceTV      DeviceCategory = "tv"
	DeviceBot     DeviceCategory = "bot"
	DeviceUnknown DeviceCategory = "unknown"
)

// Dimension names an analytics breakdown axis.
type Dimension string

const (
	DimTotal          Dimension = "total"
	DimUAFamily       Dimension = "ua_family"
	DimDeviceCategory Dimension = "device_category"
	DimReferrerHost   Dimension = "referrer_host"
	DimIsBot          Dimension = "is_bot"
)

// RollupDelta is one increment to a (code, hour, dimension, value) aggregate. The analytics
// pipeline computes these per batch and the store applies them in the SAME transaction as
// the raw events, so breakdowns equal totals by construction (data-model.md).
type RollupDelta struct {
	CodeID     string
	HourBucket time.Time
	Dimension  Dimension
	Value      string
	Count      int64
}

// ScanBatch is what the store persists atomically: raw events plus their rollup deltas.
type ScanBatch struct {
	Events  []ScanEvent
	Rollups []RollupDelta
}

// Bucket is the time-series granularity for analytics queries.
type Bucket string

const (
	BucketHour Bucket = "hour"
	BucketDay  Bucket = "day"
	BucketWeek Bucket = "week"
)

// AnalyticsQuery selects a code and a time range.
type AnalyticsQuery struct {
	CodeID string
	From   time.Time
	To     time.Time
	Bucket Bucket
}

// SeriesPoint is one bucket in the trend.
type SeriesPoint struct {
	Start time.Time
	Count int64
}

// BreakdownValue is one row of a dimension breakdown.
type BreakdownValue struct {
	Value string
	Count int64
}

// AnalyticsResult is the aggregate view for a code over a range. Every breakdown's counts
// sum to Total for the same range; the store contract suite asserts it.
type AnalyticsResult struct {
	Total      int64
	Series     []SeriesPoint
	Breakdowns map[Dimension][]BreakdownValue
}

// CodeFilter drives paginated listing (FR-016). Cursor is opaque to callers.
type CodeFilter struct {
	UserID        string
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	Destination   string // substring match on destination; empty = no filter
	Limit         int
	Cursor        string
}

// Recorder accepts scan events off the request path. Implementations MUST return
// immediately and MUST NOT block (Principle IV); the analytics package drops on a full
// buffer. A NopRecorder satisfies this until analytics is wired.
type Recorder interface {
	Record(ev ScanEvent)
}

// NopRecorder discards every event.
type NopRecorder struct{}

// Record implements Recorder.
func (NopRecorder) Record(ScanEvent) {}
