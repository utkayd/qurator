package console

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/utkayd/qurator/internal/domain"
)

// This file defines the interfaces the console needs from the rest of the system. The
// console calls the SAME service layer the HTTP API calls — never HTTP itself — so these
// are minimal method sets that another stream's real service types are expected to
// satisfy (directly, or via a small adapter written at wiring time). Tests in this
// package use fakes that implement these interfaces.

// StylingInput is the styling a caller requests when creating a code or an ephemeral
// preview. It intentionally excludes fields that only exist once a code is persisted
// (effective EC level, logo blob key).
type StylingInput struct {
	FgColor       string
	BgColor       string
	ModuleShape   domain.ModuleShape
	MarginModules int
	SizePx        int
	ECLevel       domain.ECLevel
}

// CreateCodeInput is what the console form collects to create a dynamic code.
type CreateCodeInput struct {
	Destination string
	Alias       string // empty = system-generated short code
	Styling     StylingInput
}

// CodePage is one page of a user's codes plus an opaque cursor for the next page.
type CodePage struct {
	Items      []domain.Code
	NextCursor string
}

// CodesService is the subset of the codes capability the console needs.
type CodesService interface {
	List(ctx context.Context, userID string, filter domain.CodeFilter) (CodePage, error)
	Create(ctx context.Context, userID string, in CreateCodeInput) (domain.Code, error)
	Get(ctx context.Context, userID, id string) (domain.Code, error)
	// UpdateDestination changes only the destination. ifMatchVersion, when non-nil, is
	// sent as the optimistic-concurrency check; a losing write returns ErrVersionConflict.
	UpdateDestination(ctx context.Context, userID, id, destination string, ifMatchVersion *int64) (domain.Code, error)
	Delete(ctx context.Context, userID, id string) error
}

// TokensService is the subset of the API-token capability the console needs.
type TokensService interface {
	List(ctx context.Context, userID string) ([]domain.APIToken, error)
	// Create returns the persisted token metadata and the plaintext secret. The secret
	// exists only in this return value — it is never retrievable again.
	Create(ctx context.Context, userID, name string, expiresAt *time.Time) (domain.APIToken, string, error)
	Revoke(ctx context.Context, userID, id string) error
}

// AnalyticsService is the subset of the analytics capability the console needs.
type AnalyticsService interface {
	Get(ctx context.Context, userID string, q domain.AnalyticsQuery) (domain.AnalyticsResult, error)
}

// Authenticator lets the console authenticate a sign-in form and resolve the current
// caller from a request, without the console needing to know how sessions are
// implemented (cookie format, JWT claims, etc. belong to the auth stream).
type Authenticator interface {
	// SignIn verifies credentials and, on success, sets whatever session cookie the auth
	// stream uses on w.
	SignIn(ctx context.Context, w http.ResponseWriter, email, password string) (domain.User, error)
	// CurrentUser resolves the caller from the request's session cookie (or any other
	// identity the auth middleware has already attached to its context). ok is false for
	// an anonymous request.
	CurrentUser(r *http.Request) (domain.User, bool)
	// SignOut clears the session.
	SignOut(w http.ResponseWriter, r *http.Request)
}

// Deps wires the console to the rest of the system. Every field is required in
// production; tests supply fakes.
type Deps struct {
	Codes     CodesService
	Tokens    TokensService
	Analytics AnalyticsService
	Auth      Authenticator
}

// Sentinel errors a CodesService/TokensService/AnalyticsService/Authenticator
// implementation may return; the handler maps them to the right console-side
// presentation (a form error, a 409 banner, a redirect to sign-in). Wiring adapters over
// the real service layer are expected to translate their own error types to these.
var (
	// ErrNotFound means no such resource, or it exists but is not visible to this
	// caller — the console never distinguishes the two (contracts/errors.md).
	ErrNotFound = errors.New("console: not found")
	// ErrVersionConflict means an If-Match optimistic-concurrency check lost the race.
	ErrVersionConflict = errors.New("console: version conflict")
	// ErrInvalidCredentials means SignIn was called with a wrong email/password.
	ErrInvalidCredentials = errors.New("console: invalid credentials")
	// ErrValidation means the input failed a domain validation rule (bad destination
	// scheme, alias taken, contrast too low, etc). Implementations should wrap it with
	// a safe, user-facing message via fmt.Errorf("%w: ...", ErrValidation).
	ErrValidation = errors.New("console: validation failed")
)
