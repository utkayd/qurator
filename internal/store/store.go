package store

import (
	"context"
	"time"

	"github.com/utkayd/qurator/internal/domain"
)

// Store is the metadata persistence contract. Every driver must pass
// storetest.RunStoreContract unmodified. See contracts/store.md for the twelve
// behaviours the suite pins and why each exists.
type Store interface {
	// Users
	CreateUser(ctx context.Context, u *domain.User) error
	GetUserByID(ctx context.Context, id string) (*domain.User, error)
	GetUserByEmail(ctx context.Context, email string) (*domain.User, error) // case-insensitive
	BumpTokenVersion(ctx context.Context, userID string) (int64, error)
	CountUsers(ctx context.Context) (int64, error)

	// API tokens
	CreateToken(ctx context.Context, t *domain.APIToken) error
	GetTokenByID(ctx context.Context, id string) (*domain.APIToken, error)
	ListTokens(ctx context.Context, userID string) ([]*domain.APIToken, error)
	RevokeToken(ctx context.Context, id, userID string) error
	TouchTokenLastUsed(ctx context.Context, id string, at time.Time) error

	// Codes. CreateCode persists the styling profile in the same transaction and reserves
	// the short code. GetCodeByID with a non-owner userID returns ErrNotFound, never the
	// row — an existence oracle would let one user enumerate another's codes.
	CreateCode(ctx context.Context, c *domain.Code) error
	GetCodeByShortCode(ctx context.Context, shortCode string) (*domain.Code, error) // case-insensitive
	GetCodeByID(ctx context.Context, id, userID string) (*domain.Code, error)
	ListCodes(ctx context.Context, f domain.CodeFilter) (items []*domain.Code, nextCursor string, err error)
	// UpdateDestination applies only when the row's version equals expectedVersion, then
	// increments it; zero rows affected is ErrConflict.
	UpdateDestination(ctx context.Context, id, userID, dest string, expectedVersion int64) error
	SetCodeState(ctx context.Context, id, userID string, state domain.CodeState) error
	// DeleteCode is a soft delete: state becomes deleted and the alias reservation
	// survives (FR-018).
	DeleteCode(ctx context.Context, id, userID string) error

	// Aliases
	IsAliasAvailable(ctx context.Context, shortCode string) (bool, error)
	// ReleaseAlias frees a reserved short code whose owning code is deleted; ErrConflict
	// if the code is live, ErrNotFound if nothing is reserved.
	ReleaseAlias(ctx context.Context, shortCode string) error

	// Analytics. InsertScanBatch writes events and rollup deltas atomically.
	InsertScanBatch(ctx context.Context, b domain.ScanBatch) error
	QueryAnalytics(ctx context.Context, q domain.AnalyticsQuery) (*domain.AnalyticsResult, error)
	// PruneScanEvents deletes at most limit raw events older than before and reports how
	// many were removed. Rollups are never touched.
	PruneScanEvents(ctx context.Context, before time.Time, limit int) (int64, error)

	// Lifecycle
	Migrate(ctx context.Context) error
	Ping(ctx context.Context) error
	Close() error
}
