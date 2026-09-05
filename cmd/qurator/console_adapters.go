package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/utkayd/qurator/internal/auth"
	"github.com/utkayd/qurator/internal/codes"
	"github.com/utkayd/qurator/internal/console"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/store"
)

// Adapters that satisfy the console's narrow service interfaces over the real
// services. They translate errors into the console's sentinels so templates can
// render safe messages; internal causes are never surfaced.

// --- codes ---

type consoleCodes struct{ svc *codes.Service }

func (c consoleCodes) List(ctx context.Context, userID string, f domain.CodeFilter) (console.CodePage, error) {
	f.UserID = userID
	items, next, err := c.svc.List(ctx, f)
	if err != nil {
		return console.CodePage{}, err
	}
	page := console.CodePage{Items: make([]domain.Code, 0, len(items)), NextCursor: next}
	for _, it := range items {
		page.Items = append(page.Items, *it)
	}
	return page, nil
}

func (c consoleCodes) Create(ctx context.Context, userID string, in console.CreateCodeInput) (domain.Code, error) {
	out, err := c.svc.Create(ctx, codes.CreateInput{
		UserID:      userID,
		Destination: in.Destination,
		Alias:       in.Alias,
		Mode:        domain.CodeMode(in.Mode), // empty → dynamic; the service validates the value
		Styling: domain.Styling{
			FgColor:       in.Styling.FgColor,
			BgColor:       in.Styling.BgColor,
			ModuleShape:   in.Styling.ModuleShape,
			MarginModules: in.Styling.MarginModules,
			SizePx:        in.Styling.SizePx,
			ECLevel:       in.Styling.ECLevel,
		},
	})
	if err != nil {
		return domain.Code{}, translateCodesErr(err)
	}
	return *out, nil
}

func (c consoleCodes) Get(ctx context.Context, userID, id string) (domain.Code, error) {
	out, err := c.svc.Get(ctx, id, userID)
	if err != nil {
		return domain.Code{}, translateCodesErr(err)
	}
	return *out, nil
}

func (c consoleCodes) UpdateDestination(ctx context.Context, userID, id, dest string, ifMatch *int64) (domain.Code, error) {
	expected := int64(0)
	if ifMatch != nil {
		expected = *ifMatch
	} else {
		cur, err := c.svc.Get(ctx, id, userID)
		if err != nil {
			return domain.Code{}, translateCodesErr(err)
		}
		expected = cur.Version
	}
	out, err := c.svc.UpdateDestination(ctx, id, userID, dest, expected)
	if err != nil {
		return domain.Code{}, translateCodesErr(err)
	}
	return *out, nil
}

func (c consoleCodes) Delete(ctx context.Context, userID, id string) error {
	return translateCodesErr(c.svc.Delete(ctx, id, userID))
}

func translateCodesErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return console.ErrNotFound
	case errors.Is(err, store.ErrConflict):
		return console.ErrVersionConflict
	case errors.Is(err, store.ErrAliasTaken):
		return fmt.Errorf("%w: that alias is already in use", console.ErrValidation)
	case errors.Is(err, codes.ErrAliasReserved):
		return fmt.Errorf("%w: that alias is reserved", console.ErrValidation)
	case errors.Is(err, codes.ErrAliasInvalid):
		return fmt.Errorf("%w: aliases are 3–64 characters of a-z, 0-9 and hyphens", console.ErrValidation)
	case errors.Is(err, codes.ErrUnsupportedScheme):
		return fmt.Errorf("%w: the destination must use an allowed scheme", console.ErrValidation)
	case errors.Is(err, codes.ErrSelfReferential):
		return fmt.Errorf("%w: the destination cannot point back at this instance", console.ErrValidation)
	case errors.Is(err, codes.ErrInvalidDestination):
		return fmt.Errorf("%w: the destination is not a valid URL", console.ErrValidation)
	case errors.Is(err, codes.ErrInvalidStyling):
		return fmt.Errorf("%w: the styling is not valid", console.ErrValidation)
	case errors.Is(err, codes.ErrInvalidMode):
		return fmt.Errorf("%w: mode must be dynamic or direct", console.ErrValidation)
	case errors.Is(err, codes.ErrDirectImmutable):
		return fmt.Errorf("%w: this is a direct code; its destination is printed into the image and cannot be changed", console.ErrValidation)
	}
	return err
}

// --- tokens ---

type consoleTokens struct {
	a  *auth.Authenticator
	st store.Store
}

func (t consoleTokens) List(ctx context.Context, userID string) ([]domain.APIToken, error) {
	items, err := t.st.ListTokens(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.APIToken, 0, len(items))
	for _, it := range items {
		out = append(out, *it)
	}
	return out, nil
}

func (t consoleTokens) Create(ctx context.Context, userID, name string, expiresAt *time.Time) (domain.APIToken, string, error) {
	tok, secret, err := t.a.CreateToken(ctx, userID, name, expiresAt)
	if err != nil {
		return domain.APIToken{}, "", err
	}
	return *tok, secret, nil
}

func (t consoleTokens) Revoke(ctx context.Context, userID, id string) error {
	err := t.st.RevokeToken(ctx, id, userID)
	if errors.Is(err, store.ErrNotFound) {
		return console.ErrNotFound
	}
	return err
}

// --- analytics ---

type consoleAnalytics struct{ st store.Store }

func (a consoleAnalytics) Get(ctx context.Context, userID string, q domain.AnalyticsQuery) (domain.AnalyticsResult, error) {
	// Ownership check first: never leak another user's analytics.
	if _, err := a.st.GetCodeByID(ctx, q.CodeID, userID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.AnalyticsResult{}, console.ErrNotFound
		}
		return domain.AnalyticsResult{}, err
	}
	res, err := a.st.QueryAnalytics(ctx, q)
	if err != nil {
		return domain.AnalyticsResult{}, err
	}
	return *res, nil
}

// --- auth ---

type consoleAuth struct {
	a  *auth.Authenticator
	st store.Store
}

func (c consoleAuth) SignIn(ctx context.Context, w http.ResponseWriter, email, password string) (domain.User, error) {
	email = strings.TrimSpace(email)
	u, err := c.st.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			auth.DummyVerify(password) // uniform timing for unknown email
			return domain.User{}, console.ErrInvalidCredentials
		}
		return domain.User{}, err
	}
	if u.PasswordHash == "" {
		auth.DummyVerify(password)
		return domain.User{}, console.ErrInvalidCredentials
	}
	ok, err := auth.VerifyPassword(u.PasswordHash, password)
	if err != nil {
		return domain.User{}, err
	}
	if !ok {
		return domain.User{}, console.ErrInvalidCredentials
	}
	token, exp, err := c.a.IssueSession(u)
	if err != nil {
		return domain.User{}, err
	}
	http.SetCookie(w, c.a.SessionCookie(token, exp))
	return *u, nil
}

func (c consoleAuth) CurrentUser(r *http.Request) (domain.User, bool) {
	id, ok := auth.IdentityFrom(r.Context())
	if !ok {
		return domain.User{}, false
	}
	u, err := c.st.GetUserByID(r.Context(), id.UserID)
	if err != nil {
		return domain.User{}, false
	}
	return *u, true
}

func (c consoleAuth) SignOut(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, auth.ClearSessionCookie())
}

func newConsoleDeps(svc *codes.Service, a *auth.Authenticator, st store.Store) console.Deps {
	return console.Deps{
		Codes:     consoleCodes{svc: svc},
		Tokens:    consoleTokens{a: a, st: st},
		Analytics: consoleAnalytics{st: st},
		Auth:      consoleAuth{a: a, st: st},
	}
}
