package console

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/utkayd/qurator/internal/domain"
)

// F13: the list page must not grow with the destination. A long destination is cut to
// a bounded prefix with an ellipsis; the title attribute keeps only the same bounded
// text so the page stays bounded even when the cell is hovered.
func TestCodesListTruncatesLongDestinations(t *testing.T) {
	deps, auth, codes, _ := newTestDeps()
	auth.addUser(domain.User{ID: "usr_1", Email: "admin@example.com"}, "hunter2")
	long := "https://example.com/" + strings.Repeat("x", 3000)
	if _, err := codes.Create(t.Context(), "usr_1", CreateCodeInput{Destination: long}); err != nil {
		t.Fatalf("create: %v", err)
	}
	h := New(deps)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, newSignedInRequest(t, auth, http.MethodGet, "/ui/"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Contains(body, long) {
		t.Fatalf("list page renders the full %d-char destination", len(long))
	}
	want := truncate(long, listDestinationMax)
	if !strings.HasSuffix(want, "…") || len([]rune(want)) != listDestinationMax+1 {
		t.Fatalf("truncate(%d chars) = %d runes, want %d plus an ellipsis", len(long), len([]rune(want)), listDestinationMax)
	}
	if !strings.Contains(body, want) {
		t.Fatalf("list page does not contain the truncated destination %q", want)
	}
	if strings.Count(body, "https://example.com/xxxx") != 2 { // title attribute + cell text
		t.Fatalf("expected the bounded destination exactly twice (title + cell), got %d", strings.Count(body, "https://example.com/xxxx"))
	}
}

func TestTruncateKeepsShortValues(t *testing.T) {
	if got := truncate("https://example.com/", 80); got != "https://example.com/" {
		t.Fatalf("short value must be returned unchanged, got %q", got)
	}
	if got := truncate("héllo wörld", 5); got != "héllo…" {
		t.Fatalf("truncate must count runes, got %q", got)
	}
}
