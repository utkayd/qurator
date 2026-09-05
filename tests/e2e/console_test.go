// Package e2e exercises the console over real HTTP, the way a browser (or a browser
// minus its JavaScript engine, since this test has none) would: cookies via a jar,
// requests via net/http, and HTML parsed structurally via golang.org/x/net/html rather
// than by substring matching wherever a real assertion is possible.
//
// It builds internal/console.Handler directly against small in-memory fakes of the
// service-layer interfaces the console defines (internal/console/types.go) — it does not
// depend on any other stream's implementation, per this stream's isolation contract.
package e2e

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/utkayd/qurator/internal/console"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi/middleware"
)

// ---------------------------------------------------------------------------
// Fakes — this stream's own implementations of the interfaces the console defines.
// ---------------------------------------------------------------------------

type e2eAuth struct {
	mu    sync.Mutex
	users map[string]struct {
		user     domain.User
		password string
	}
	sessions map[string]domain.User
	next     int
}

const sessionCookieName = "qurator_e2e_session"

func newE2EAuth() *e2eAuth {
	a := &e2eAuth{sessions: map[string]domain.User{}}
	a.users = map[string]struct {
		user     domain.User
		password string
	}{}
	return a
}

func (a *e2eAuth) addUser(u domain.User, password string) {
	a.users[u.Email] = struct {
		user     domain.User
		password string
	}{u, password}
}

func (a *e2eAuth) SignIn(_ context.Context, w http.ResponseWriter, email, password string) (domain.User, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, ok := a.users[email]
	if !ok || rec.password != password {
		return domain.User{}, fmt.Errorf("invalid credentials")
	}
	a.next++
	token := strconv.Itoa(a.next)
	a.sessions[token] = rec.user
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	return rec.user, nil
}

func (a *e2eAuth) CurrentUser(r *http.Request) (domain.User, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return domain.User{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	u, ok := a.sessions[c.Value]
	return u, ok
}

func (a *e2eAuth) SignOut(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1})
}

type e2eCodes struct {
	mu    sync.Mutex
	codes map[string]domain.Code
	next  int
}

func newE2ECodes() *e2eCodes { return &e2eCodes{codes: map[string]domain.Code{}} }

func (c *e2eCodes) List(_ context.Context, userID string, _ domain.CodeFilter) (console.CodePage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var items []domain.Code
	for _, code := range c.codes {
		if code.UserID == userID && code.State != domain.CodeDeleted {
			items = append(items, code)
		}
	}
	return console.CodePage{Items: items}, nil
}

func (c *e2eCodes) Create(_ context.Context, userID string, in console.CreateCodeInput) (domain.Code, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	id := fmt.Sprintf("cod_e2e%d", c.next)
	short := in.Alias
	if short == "" {
		short = fmt.Sprintf("short-e2e-%d", c.next)
	}
	now := time.Now().UTC()
	code := domain.Code{
		ID:          id,
		ShortCode:   short,
		IsAlias:     in.Alias != "",
		UserID:      userID,
		Destination: in.Destination,
		State:       domain.CodeActive,
		Styling: domain.Styling{
			FgColor: in.Styling.FgColor, BgColor: in.Styling.BgColor,
			ModuleShape: in.Styling.ModuleShape, MarginModules: in.Styling.MarginModules,
			SizePx: in.Styling.SizePx, ECLevel: in.Styling.ECLevel, ECLevelEffective: in.Styling.ECLevel,
		},
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	c.codes[id] = code
	return code, nil
}

func (c *e2eCodes) Get(_ context.Context, userID, id string) (domain.Code, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	code, ok := c.codes[id]
	if !ok || code.UserID != userID {
		return domain.Code{}, console.ErrNotFound
	}
	return code, nil
}

func (c *e2eCodes) UpdateDestination(_ context.Context, userID, id, destination string, ifMatch *int64) (domain.Code, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	code, ok := c.codes[id]
	if !ok || code.UserID != userID {
		return domain.Code{}, console.ErrNotFound
	}
	if ifMatch != nil && *ifMatch != code.Version {
		return domain.Code{}, console.ErrVersionConflict
	}
	code.Destination = destination
	code.Version++
	c.codes[id] = code
	return code, nil
}

func (c *e2eCodes) Delete(_ context.Context, userID, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	code, ok := c.codes[id]
	if !ok || code.UserID != userID {
		return console.ErrNotFound
	}
	code.State = domain.CodeDeleted
	c.codes[id] = code
	return nil
}

type e2eTokens struct {
	mu     sync.Mutex
	tokens map[string]domain.APIToken
	next   int
}

func newE2ETokens() *e2eTokens { return &e2eTokens{tokens: map[string]domain.APIToken{}} }

func (t *e2eTokens) List(_ context.Context, userID string) ([]domain.APIToken, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []domain.APIToken
	for _, tok := range t.tokens {
		if tok.UserID == userID {
			out = append(out, tok)
		}
	}
	return out, nil
}

func (t *e2eTokens) Create(_ context.Context, userID, name string, expiresAt *time.Time) (domain.APIToken, string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.next++
	id := fmt.Sprintf("tok_e2e%d", t.next)
	tok := domain.APIToken{ID: id, UserID: userID, Name: name, CreatedAt: time.Now().UTC(), ExpiresAt: expiresAt}
	t.tokens[id] = tok
	return tok, fmt.Sprintf("qur_e2e_secret_%d", t.next), nil
}

func (t *e2eTokens) Revoke(_ context.Context, userID, id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	tok, ok := t.tokens[id]
	if !ok || tok.UserID != userID {
		return console.ErrNotFound
	}
	now := time.Now().UTC()
	tok.RevokedAt = &now
	t.tokens[id] = tok
	return nil
}

type e2eAnalytics struct{}

func (e2eAnalytics) Get(_ context.Context, _ string, q domain.AnalyticsQuery) (domain.AnalyticsResult, error) {
	return domain.AnalyticsResult{
		Total: 7,
		Series: []domain.SeriesPoint{
			{Start: q.From, Count: 3},
			{Start: q.From.AddDate(0, 0, 1), Count: 4},
		},
		Breakdowns: map[domain.Dimension][]domain.BreakdownValue{
			domain.DimDeviceCategory: {{Value: "mobile", Count: 7}},
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Test harness
// ---------------------------------------------------------------------------

func newTestServer(t *testing.T) (*httptest.Server, *http.Client, *e2eAuth) {
	t.Helper()
	auth := newE2EAuth()
	deps := console.Deps{
		Codes:     newE2ECodes(),
		Tokens:    newE2ETokens(),
		Analytics: e2eAnalytics{},
		Auth:      auth,
	}
	h := console.New(deps)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	client := &http.Client{Jar: jar}
	return srv, client, auth
}

func mustParseHTML(t *testing.T, body string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parsing HTML: %v", err)
	}
	return doc
}

func attr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

// findAll returns every node matching pred, depth-first.
func findAll(n *html.Node, pred func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if pred(n) {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func doRequest(t *testing.T, client *http.Client, method, target string, body url.Values, csrf bool, extraHeaders map[string]string) *http.Response {
	t.Helper()
	var reqBody *strings.Reader
	if body != nil {
		reqBody = strings.NewReader(body.Encode())
	} else {
		reqBody = strings.NewReader("")
	}
	req, err := http.NewRequest(method, target, reqBody)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if csrf {
		req.Header.Set(middleware.CSRFHeader, "htmx")
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	return resp
}

// ---------------------------------------------------------------------------
// The full lifecycle, US6's independent test.
// ---------------------------------------------------------------------------

func TestConsoleLifecycle(t *testing.T) {
	srv, client, auth := newTestServer(t)
	auth.addUser(domain.User{ID: "usr_1", Email: "owner@example.com"}, "correct horse battery staple")

	// 1. Anonymous request to any /ui/* route (other than sign-in) redirects to sign-in.
	resp := doRequest(t, client, http.MethodGet, srv.URL+"/ui/", nil, false, nil)
	if resp.Request.URL.Path != "/ui/signin" {
		t.Fatalf("anonymous / did not land on sign-in, got %s", resp.Request.URL.Path)
	}
	_ = resp.Body.Close()

	// 2. Sign in via the plain HTML form.
	resp = doRequest(t, client, http.MethodPost, srv.URL+"/ui/signin", url.Values{
		"email":    {"owner@example.com"},
		"password": {"correct horse battery staple"},
	}, false, nil)
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/ui/" {
		t.Fatalf("sign-in failed: status=%d path=%s", resp.StatusCode, resp.Request.URL.Path)
	}
	_ = resp.Body.Close()

	// 3. Create a styled dynamic code.
	resp = doRequest(t, client, http.MethodPost, srv.URL+"/ui/codes", url.Values{
		"destination":    {"https://example.com/spring-sale"},
		"alias":          {"spring-sale"},
		"format":         {"png"},
		"fg_color":       {"#101828"},
		"bg_color":       {"#ffffff"},
		"module_shape":   {"rounded"},
		"margin_modules": {"4"},
		"size_px":        {"512"},
		"ec_level":       {"Q"},
	}, true, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create code: status %d", resp.StatusCode)
	}
	codePath := resp.Request.URL.Path // followed the 303 to /ui/codes/{id}
	if !strings.HasPrefix(codePath, "/ui/codes/") {
		t.Fatalf("expected redirect to code detail, got %s", codePath)
	}
	_ = resp.Body.Close()

	// 4. The list shows it.
	resp = doRequest(t, client, http.MethodGet, srv.URL+"/ui/", nil, false, nil)
	listBody := readBody(t, resp)
	if !strings.Contains(listBody, "spring-sale") {
		t.Fatalf("codes list does not mention the created code:\n%s", listBody)
	}

	// 5. Open the detail page; find the destination form's optimistic-concurrency token
	// and the delete button's confirmation text.
	resp = doRequest(t, client, http.MethodGet, srv.URL+codePath, nil, false, nil)
	detailBody := readBody(t, resp)
	doc := mustParseHTML(t, detailBody)

	ifMatchNodes := findAll(doc, func(n *html.Node) bool {
		_, ok := attr(n, "data-if-match")
		return ok
	})
	if len(ifMatchNodes) == 0 {
		t.Fatalf("no data-if-match element found on code detail page")
	}
	ifMatch, _ := attr(ifMatchNodes[0], "data-if-match")

	confirmNodes := findAll(doc, func(n *html.Node) bool {
		_, ok := attr(n, "data-confirm-message")
		return ok
	})
	if len(confirmNodes) == 0 {
		t.Fatalf("no delete confirmation element found on code detail page")
	}
	confirmMsg, _ := attr(confirmNodes[0], "data-confirm-message")
	if !strings.Contains(strings.ToLower(confirmMsg), "print") {
		t.Fatalf("delete confirmation does not mention already-printed codes: %q", confirmMsg)
	}

	// Analytics chart is present as inline SVG.
	if !strings.Contains(detailBody, "<svg") {
		t.Fatalf("code detail page has no inline SVG analytics chart:\n%s", detailBody)
	}

	// 6. Edit the destination via PATCH with the If-Match token, as the console's own JS
	// would send it.
	resp = doRequest(t, client, http.MethodPatch, srv.URL+codePath, url.Values{
		"destination": {"https://example.com/spring-sale-2026"},
	}, true, map[string]string{"If-Match": `"` + ifMatch + `"`})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update destination: status %d, body: %s", resp.StatusCode, readBody(t, resp))
	}
	_ = resp.Body.Close()

	resp = doRequest(t, client, http.MethodGet, srv.URL+codePath, nil, false, nil)
	detailBody = readBody(t, resp)
	if !strings.Contains(detailBody, "spring-sale-2026") {
		t.Fatalf("destination was not updated:\n%s", detailBody)
	}

	// 7. Create a token; the secret is shown exactly once.
	resp = doRequest(t, client, http.MethodPost, srv.URL+"/ui/tokens", url.Values{
		"name": {"ci-pipeline"},
	}, true, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create token: status %d, body: %s", resp.StatusCode, readBody(t, resp))
	}
	createdBody := readBody(t, resp)
	tokDoc := mustParseHTML(t, createdBody)
	secretNodes := findAll(tokDoc, func(n *html.Node) bool {
		_, ok := attr(n, "data-secret-value")
		return ok
	})
	if len(secretNodes) != 1 {
		t.Fatalf("expected exactly one data-secret-value element, found %d", len(secretNodes))
	}
	secret := strings.TrimSpace(textContent(secretNodes[0]))
	if !strings.HasPrefix(secret, "qur_e2e_secret_") {
		t.Fatalf("unexpected secret value: %q", secret)
	}

	// Reloading the token list never shows the secret again.
	resp = doRequest(t, client, http.MethodGet, srv.URL+"/ui/tokens", nil, false, nil)
	tokensBody := readBody(t, resp)
	if strings.Contains(tokensBody, secret) {
		t.Fatalf("token secret leaked into the token list page")
	}
	if !strings.Contains(tokensBody, "ci-pipeline") {
		t.Fatalf("token list does not show the created token's name")
	}

	// Find the token's revoke form to get its id.
	tokListDoc := mustParseHTML(t, tokensBody)
	revokeForms := findAll(tokListDoc, func(n *html.Node) bool {
		action, ok := attr(n, "hx-delete")
		return ok && strings.HasPrefix(action, "/ui/tokens/")
	})
	if len(revokeForms) != 1 {
		t.Fatalf("expected exactly one revoke form, found %d", len(revokeForms))
	}
	revokePath, _ := attr(revokeForms[0], "hx-delete")

	// 8. Revoke it.
	resp = doRequest(t, client, http.MethodDelete, srv.URL+revokePath, nil, true, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke token: status %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp = doRequest(t, client, http.MethodGet, srv.URL+"/ui/tokens", nil, false, nil)
	tokensBody = readBody(t, resp)
	if !strings.Contains(tokensBody, "revoked") {
		t.Fatalf("token list does not show the revoked status:\n%s", tokensBody)
	}

	// 9. Delete the code. The confirmation text was already verified in step 5; this
	// exercises the actual destructive action.
	resp = doRequest(t, client, http.MethodDelete, srv.URL+codePath, nil, true, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete code: status %d", resp.StatusCode)
	}
	if resp.Request.URL.Path != "/ui/" {
		t.Fatalf("delete code did not redirect to the list, got %s", resp.Request.URL.Path)
	}
	finalListBody := readBody(t, resp)
	if strings.Contains(finalListBody, "spring-sale-2026") {
		t.Fatalf("deleted code's destination still appears in the list")
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return b.String()
}
