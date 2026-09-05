package observability

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	dto "github.com/prometheus/client_model/go"
)

func TestMiddlewareLabelsByRoutePatternNotPath(t *testing.T) {
	m := NewMetrics()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /r/{code}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := m.Middleware(mux)

	for i := 0; i < 100; i++ {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/r/code%d", i), nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, rec.Code)
		}
	}

	families, err := m.registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	counterFamily := findFamily(t, families, "qurator_http_requests_total")

	routes := map[string]struct{}{}
	for _, metric := range counterFamily.GetMetric() {
		routes[labelValue(metric.GetLabel(), "route")] = struct{}{}
	}

	if len(routes) != 1 {
		t.Fatalf("expected exactly one route label series, got %d: %v", len(routes), routes)
	}
	if _, ok := routes["GET /r/{code}"]; !ok {
		t.Fatalf(`expected route label "GET /r/{code}", got %v`, routes)
	}

	// Sanity: 100 distinct requests collapsed onto that single series.
	var total float64
	for _, metric := range counterFamily.GetMetric() {
		total += metric.GetCounter().GetValue()
	}
	if total != 100 {
		t.Fatalf("expected 100 total requests recorded, got %v", total)
	}
}

func TestHistogramBucketBoundaries(t *testing.T) {
	m := NewMetrics()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /r/{code}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := m.Middleware(mux)

	req := httptest.NewRequest(http.MethodGet, "/r/abc", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	families, err := m.registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	histFamily := findFamily(t, families, "qurator_http_request_duration_seconds")
	if len(histFamily.GetMetric()) != 1 {
		t.Fatalf("expected 1 histogram series, got %d", len(histFamily.GetMetric()))
	}

	buckets := histFamily.GetMetric()[0].GetHistogram().GetBucket()
	want := []float64{.001, .002, .005, .01, .02, .03, .05, .075, .1, .25, .5, 1}
	if len(buckets) != len(want) {
		t.Fatalf("expected %d buckets, got %d", len(want), len(buckets))
	}
	for i, b := range buckets {
		if b.GetUpperBound() != want[i] {
			t.Fatalf("bucket %d: expected upper bound %v, got %v", i, want[i], b.GetUpperBound())
		}
	}
}

func findFamily(t *testing.T, families []*dto.MetricFamily, name string) *dto.MetricFamily {
	t.Helper()
	for _, f := range families {
		if f.GetName() == name {
			return f
		}
	}
	t.Fatalf("metric family %q not found", name)
	return nil
}

func labelValue(labels []*dto.LabelPair, name string) string {
	for _, l := range labels {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}
