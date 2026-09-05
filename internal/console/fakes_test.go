package console

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/utkayd/qurator/internal/domain"
)

// fakeAuth is an in-memory Authenticator. Sessions are tracked by an opaque cookie
// value; there is no real cryptography here because these tests exercise the console's
// own logic, not the auth stream's.
type fakeAuth struct {
	mu       sync.Mutex
	users    map[string]fakeUser // email -> user+password
	sessions map[string]domain.User
	nextSess int
}

type fakeUser struct {
	user     domain.User
	password string
}

func newFakeAuth() *fakeAuth {
	return &fakeAuth{
		users:    map[string]fakeUser{},
		sessions: map[string]domain.User{},
	}
}

func (f *fakeAuth) addUser(u domain.User, password string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[u.Email] = fakeUser{user: u, password: password}
}

const fakeSessionCookie = "qurator_console_test_session"

func (f *fakeAuth) SignIn(_ context.Context, w http.ResponseWriter, email, password string) (domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fu, ok := f.users[email]
	if !ok || fu.password != password {
		return domain.User{}, ErrInvalidCredentials
	}
	f.nextSess++
	token := strconv.Itoa(f.nextSess)
	f.sessions[token] = fu.user
	http.SetCookie(w, &http.Cookie{
		Name:     fakeSessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	return fu.user, nil
}

func (f *fakeAuth) CurrentUser(r *http.Request) (domain.User, bool) {
	c, err := r.Cookie(fakeSessionCookie)
	if err != nil {
		return domain.User{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.sessions[c.Value]
	return u, ok
}

func (f *fakeAuth) SignOut(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(fakeSessionCookie); err == nil {
		f.mu.Lock()
		delete(f.sessions, c.Value)
		f.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     fakeSessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// fakeCodes is an in-memory CodesService.
type fakeCodes struct {
	mu    sync.Mutex
	codes map[string]domain.Code
	next  int
}

func newFakeCodes() *fakeCodes {
	return &fakeCodes{codes: map[string]domain.Code{}}
}

func (f *fakeCodes) List(_ context.Context, userID string, filter domain.CodeFilter) (CodePage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var items []domain.Code
	for _, c := range f.codes {
		if c.UserID == userID {
			items = append(items, c)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	return CodePage{Items: items}, nil
}

func (f *fakeCodes) Create(_ context.Context, userID string, in CreateCodeInput) (domain.Code, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if in.Destination == "" {
		return domain.Code{}, fmt.Errorf("%w: a destination is required", ErrValidation)
	}
	f.next++
	id := fmt.Sprintf("cod_test%d", f.next)
	short := in.Alias
	if short == "" {
		short = fmt.Sprintf("short%d", f.next)
	}
	mode := in.Mode
	if mode == "" {
		mode = modeDynamic
	}
	now := time.Now().UTC()
	c := domain.Code{
		ID:          id,
		ShortCode:   short,
		IsAlias:     in.Alias != "",
		UserID:      userID,
		Destination: in.Destination,
		State:       domain.CodeActive,
		Mode:        domain.CodeMode(mode),
		Styling: domain.Styling{
			FgColor:          in.Styling.FgColor,
			BgColor:          in.Styling.BgColor,
			ModuleShape:      in.Styling.ModuleShape,
			MarginModules:    in.Styling.MarginModules,
			SizePx:           in.Styling.SizePx,
			ECLevel:          in.Styling.ECLevel,
			ECLevelEffective: in.Styling.ECLevel,
		},
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	f.codes[id] = c
	return c, nil
}

func (f *fakeCodes) Get(_ context.Context, userID, id string) (domain.Code, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.codes[id]
	if !ok || c.UserID != userID {
		return domain.Code{}, ErrNotFound
	}
	return c, nil
}

func (f *fakeCodes) UpdateDestination(_ context.Context, userID, id, destination string, ifMatch *int64) (domain.Code, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.codes[id]
	if !ok || c.UserID != userID {
		return domain.Code{}, ErrNotFound
	}
	if ifMatch != nil && *ifMatch != c.Version {
		return domain.Code{}, ErrVersionConflict
	}
	c.Destination = destination
	c.Version++
	c.UpdatedAt = time.Now().UTC()
	f.codes[id] = c
	return c, nil
}

func (f *fakeCodes) Delete(_ context.Context, userID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.codes[id]
	if !ok || c.UserID != userID {
		return ErrNotFound
	}
	c.State = domain.CodeDeleted
	f.codes[id] = c
	return nil
}

// fakeTokens is an in-memory TokensService.
type fakeTokens struct {
	mu     sync.Mutex
	tokens map[string]domain.APIToken
	next   int
}

func newFakeTokens() *fakeTokens {
	return &fakeTokens{tokens: map[string]domain.APIToken{}}
}

func (f *fakeTokens) List(_ context.Context, userID string) ([]domain.APIToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.APIToken
	for _, t := range f.tokens {
		if t.UserID == userID {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (f *fakeTokens) Create(_ context.Context, userID, name string, expiresAt *time.Time) (domain.APIToken, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == "" {
		return domain.APIToken{}, "", fmt.Errorf("%w: a name is required", ErrValidation)
	}
	f.next++
	id := fmt.Sprintf("tok_test%d", f.next)
	t := domain.APIToken{
		ID:        id,
		UserID:    userID,
		Name:      name,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: expiresAt,
	}
	f.tokens[id] = t
	secret := fmt.Sprintf("qur_test_secret_%d", f.next)
	return t, secret, nil
}

func (f *fakeTokens) Revoke(_ context.Context, userID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tokens[id]
	if !ok || t.UserID != userID {
		return ErrNotFound
	}
	now := time.Now().UTC()
	t.RevokedAt = &now
	f.tokens[id] = t
	return nil
}

// fakeAnalytics is a canned AnalyticsService.
type fakeAnalytics struct{}

func (fakeAnalytics) Get(_ context.Context, _ string, q domain.AnalyticsQuery) (domain.AnalyticsResult, error) {
	return domain.AnalyticsResult{
		Total: 3,
		Series: []domain.SeriesPoint{
			{Start: q.From, Count: 1},
			{Start: q.From.AddDate(0, 0, 1), Count: 2},
		},
		Breakdowns: map[domain.Dimension][]domain.BreakdownValue{
			domain.DimDeviceCategory: {{Value: "mobile", Count: 3}},
		},
	}, nil
}

func newTestDeps() (Deps, *fakeAuth, *fakeCodes, *fakeTokens) {
	auth := newFakeAuth()
	codes := newFakeCodes()
	tokens := newFakeTokens()
	return Deps{
		Codes:     codes,
		Tokens:    tokens,
		Analytics: fakeAnalytics{},
		Auth:      auth,
	}, auth, codes, tokens
}
