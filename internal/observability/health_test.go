package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type stubPinger struct {
	err error
}

func (s stubPinger) Ping(context.Context) error {
	return s.err
}

func TestHealthzAlwaysOK(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	Healthz().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestReadyzAllPassing(t *testing.T) {
	deps := map[string]Pinger{
		"store": stubPinger{},
		"blob":  stubPinger{},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	Readyz(deps, time.Second).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestReadyzFailingDependency(t *testing.T) {
	secretErr := "postgres://user:supersecret@db:5432/qurator"
	deps := map[string]Pinger{
		"store": stubPinger{err: errors.New(secretErr)},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	Readyz(deps, time.Second).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "supersecret") {
		t.Fatalf("readyz body leaked error text: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "store") {
		t.Fatalf("expected failing dependency name %q in body: %s", "store", rec.Body.String())
	}
}

func TestHealthzUnaffectedByFailingDependencies(t *testing.T) {
	// Healthz takes no dependencies at all, so this simply re-confirms it
	// stays 200 regardless of what a hypothetical Readyz check would find.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	Healthz().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}
