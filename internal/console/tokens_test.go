package console

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi/middleware"
)

// postTokenForm submits the token-creation form as a signed-in user, optionally as an
// htmx request.
func postTokenForm(t *testing.T, h *Handler, auth *fakeAuth, form url.Values, htmx bool) *httptest.ResponseRecorder {
	t.Helper()
	signin := httptest.NewRecorder()
	if _, err := auth.SignIn(t.Context(), signin, "admin@example.com", "hunter2"); err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/ui/tokens", strings.NewReader(form.Encode()))
	for _, c := range signin.Result().Cookies() {
		req.AddCookie(c)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(middleware.CSRFHeader, "1")
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// TestTokenCreateRejectsPastAndUnparsableExpiry pins F12: the console validates the
// optional expiry exactly like POST /v1/tokens does. A past instant and an unparsable
// value are both refused with a form error, and no token is created either way.
func TestTokenCreateRejectsPastAndUnparsableExpiry(t *testing.T) {
	deps, auth, _, tokens := newTestDeps()
	auth.addUser(domain.User{ID: "usr_1", Email: "admin@example.com"}, "hunter2")
	h := New(deps)

	cases := []struct {
		name    string
		expires string
		wantMsg string
	}{
		{"past", "2020-01-01T00:00", "future"},
		{"unparsable", "not-a-date", "date and time"},
		{"now-ish past with seconds", time.Now().UTC().Add(-time.Minute).Format("2006-01-02T15:04:05"), "future"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"name": {"ci-" + tc.name}, "expires_at": {tc.expires}}

			rr := postTokenForm(t, h, auth, form, false)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("plain form: status %d, want 400; body:\n%s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), `role="alert"`) || !strings.Contains(strings.ToLower(rr.Body.String()), tc.wantMsg) {
				t.Fatalf("plain form: error banner mentioning %q not rendered; body:\n%s", tc.wantMsg, rr.Body.String())
			}

			rr = postTokenForm(t, h, auth, form, true)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("htmx: status %d, want 400", rr.Code)
			}
			if rr.Header().Get("X-Qurator-Form-Error") != "true" {
				t.Fatalf("htmx: form error must be routed to the error region; headers: %v", rr.Header())
			}
			if !strings.Contains(strings.ToLower(rr.Body.String()), tc.wantMsg) {
				t.Fatalf("htmx: error fragment does not mention %q: %s", tc.wantMsg, rr.Body.String())
			}
		})
	}
	if got, _ := tokens.List(t.Context(), "usr_1"); len(got) != 0 {
		t.Fatalf("rejected submissions created %d token(s)", len(got))
	}

	// A future expiry is parsed as UTC (the label says so) and stored.
	future := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Minute)
	rr := postTokenForm(t, h, auth, url.Values{"name": {"ok"}, "expires_at": {future.Format("2006-01-02T15:04")}}, false)
	if rr.Code != http.StatusCreated {
		t.Fatalf("future expiry: status %d, body:\n%s", rr.Code, rr.Body.String())
	}
	got, _ := tokens.List(t.Context(), "usr_1")
	if len(got) != 1 || got[0].ExpiresAt == nil || !got[0].ExpiresAt.Equal(future) {
		t.Fatalf("stored token = %+v, want one token expiring at %s UTC", got, future)
	}
}

// TestTokenFormLabelsExpiryAsUTC pins that the field the server parses as UTC is
// labelled UTC, so the operator's entry and the stored instant agree.
func TestTokenFormLabelsExpiryAsUTC(t *testing.T) {
	deps, auth, _, _ := newTestDeps()
	auth.addUser(domain.User{ID: "usr_1", Email: "admin@example.com"}, "hunter2")
	h := New(deps)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, newSignedInRequest(t, auth, http.MethodGet, "/ui/tokens"))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /ui/tokens: %d", rr.Code)
	}
	body := rr.Body.String()
	i := strings.Index(body, `for="expires_at"`)
	if i < 0 {
		t.Fatalf("no expires_at label:\n%s", body)
	}
	label := body[i:]
	if j := strings.Index(label, "</label>"); j >= 0 {
		label = label[:j]
	}
	if !strings.Contains(label, "UTC") {
		t.Fatalf("expires_at label does not say UTC: %s", label)
	}
}
