package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// httpBuckets are tuned to this service's SLOs (SC-003: 50ms, SC-006: 20ms),
// not Prometheus's stock 5ms-10s buckets, which would place the whole
// latency distribution in the first two buckets. See research.md section 6.
var httpBuckets = []float64{.001, .002, .005, .01, .02, .03, .05, .075, .1, .25, .5, 1}

// Metrics holds a private Prometheus registry and the collectors registered
// against it. It deliberately does not use the global default registry so
// that tests (and multiple server instances in one process) get isolation.
type Metrics struct {
	registry *prometheus.Registry

	HTTPDuration *prometheus.HistogramVec
	HTTPRequests *prometheus.CounterVec

	Generations       prometheus.Counter
	Scans             prometheus.Counter
	ScanEventsDropped prometheus.Counter
	CacheHits         prometheus.Counter
	CacheMisses       prometheus.Counter
}

// NewMetrics constructs a Metrics with all collectors registered against a
// fresh, private registry.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		registry: reg,
		HTTPDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "qurator_http_request_duration_seconds",
			Help:    "HTTP request duration in seconds, labelled by route pattern, method and status.",
			Buckets: httpBuckets,
		}, []string{"route", "method", "status"}),
		HTTPRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "qurator_http_requests_total",
			Help: "Total HTTP requests, labelled by route pattern, method and status.",
		}, []string{"route", "method", "status"}),
		Generations: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "qurator_generations_total",
			Help: "Total number of QR codes generated.",
		}),
		Scans: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "qurator_scans_total",
			Help: "Total number of tracked-code scans recorded.",
		}),
		ScanEventsDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "qurator_scan_events_dropped_total",
			Help: "Total number of scan analytics events dropped (e.g. buffer full).",
		}),
		CacheHits: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "qurator_cache_hits_total",
			Help: "Total number of cache hits.",
		}),
		CacheMisses: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "qurator_cache_misses_total",
			Help: "Total number of cache misses.",
		}),
	}

	reg.MustRegister(
		m.HTTPDuration,
		m.HTTPRequests,
		m.Generations,
		m.Scans,
		m.ScanEventsDropped,
		m.CacheHits,
		m.CacheMisses,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	return m
}

// Handler returns an http.Handler serving this Metrics' private registry in
// the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// statusRecorder wraps an http.ResponseWriter to capture the status code
// written, while remaining transparent to http.Flusher and
// http.ResponseController (via Unwrap) so downstream handlers that rely on
// those behave unchanged.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.status = status
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	return r.ResponseWriter.Write(b)
}

// Flush implements http.Flusher, passing through to the wrapped
// ResponseWriter when it supports it.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap implements the interface used by http.ResponseController to reach
// the underlying ResponseWriter's optional interfaces (http.Hijacker,
// http.Flusher with deadlines, etc.).
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// Middleware records per-route request counts and durations. The route
// label is taken from r.Pattern — the matched ServeMux pattern (Go 1.22+),
// e.g. "GET /r/{code}" — never the concrete request path, which would create
// one Prometheus series per short code. If no pattern matched (r.Pattern is
// empty, e.g. a 404), the route label is "unmatched".
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(rec, r)
		duration := time.Since(start).Seconds()

		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}

		labels := prometheus.Labels{
			"route":  route,
			"method": r.Method,
			"status": strconv.Itoa(status),
		}

		m.HTTPDuration.With(labels).Observe(duration)
		m.HTTPRequests.With(labels).Inc()
	})
}
