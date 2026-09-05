package integration

// T107 — analytics stall never blocks a redirect (SC-003, SC-005, Constitution
// Principle IV; quickstart.md Scenario 5).
//
// The store's InsertScanBatch is replaced by one that blocks until its context expires,
// so every consumer goroutine of the recorder wedges on its first flush and the bounded
// buffer fills. 10,000 redirects are then fired from 50 goroutines through the real
// router with the real Prometheus middleware. Redirect latency must stay flat, drops
// must be counted, the code cache must keep the store out of the hot path, and a
// deadline-bounded Close must return on time with the un-persisted count visible.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/analytics"
	"github.com/utkayd/qurator/internal/codes"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi"
	"github.com/utkayd/qurator/internal/httpapi/public"
	"github.com/utkayd/qurator/internal/observability"
	"github.com/utkayd/qurator/internal/store"
	"github.com/utkayd/qurator/internal/store/storetest"
)

const (
	stRequests   = 10_000
	stGoroutines = 50
	stBuffer     = 100
)

// stCounter is an analytics.Counter backed by an atomic. privacy_test.go uses it too.
type stCounter struct{ n atomic.Int64 }

func (c *stCounter) Inc()          { c.n.Add(1) }
func (c *stCounter) Add(f float64) { c.n.Add(int64(f)) }
func (c *stCounter) Load() int64   { return c.n.Load() }

// stStallStore wraps a real in-memory Store. InsertScanBatch blocks until the caller's
// context is done (or the test releases it during cleanup) and returns ctx.Err(), which
// is exactly what a wedged database looks like from the recorder's side.
// GetCodeByShortCode is counted so the test can prove the cache carries the load.
type stStallStore struct {
	store.Store
	lookups  atomic.Int64
	batches  atomic.Int64
	released chan struct{}
}

var errStStoreReleased = errors.New("stall store released by test cleanup")

func (s *stStallStore) InsertScanBatch(ctx context.Context, _ domain.ScanBatch) error {
	s.batches.Add(1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.released:
		return errStStoreReleased
	}
}

func (s *stStallStore) GetCodeByShortCode(ctx context.Context, shortCode string) (*domain.Code, error) {
	s.lookups.Add(1)
	return s.Store.GetCodeByShortCode(ctx, shortCode)
}

// stRaceEnabled reports whether the test binary was built with -race, which slows every
// synchronised operation by roughly 5-10x and warrants a looser latency budget.
func stRaceEnabled() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range bi.Settings {
		if s.Key == "-race" && s.Value == "true" {
			return true
		}
	}
	return false
}

// stDroppedFromExposition reads qurator_scan_events_dropped_total the way an operator
// would: from the /metrics text exposition of the private registry.
func stDroppedFromExposition(t *testing.T, m *observability.Metrics) float64 {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics: status %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "qurator_scan_events_dropped_total ") {
			v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, "qurator_scan_events_dropped_total ")), 64)
			if err != nil {
				t.Fatalf("parse dropped counter %q: %v", line, err)
			}
			return v
		}
	}
	t.Fatalf("qurator_scan_events_dropped_total not found in exposition:\n%s", body)
	return 0
}

func TestStall_AnalyticsNeverBlocksRedirect(t *testing.T) {
	ctx := context.Background()

	stall := &stStallStore{Store: storetest.NewMemStore(), released: make(chan struct{})}
	t.Cleanup(func() { close(stall.released) })

	user := &domain.User{ID: domain.NewID("usr"), Email: "stall@example.test", Source: domain.UserSourceLocal}
	if err := stall.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	shortCode := "st" + strings.ToLower(domain.RandomCrockford(10))
	now := time.Now().UTC()
	code := &domain.Code{
		ID: domain.NewCodeID(), ShortCode: shortCode, UserID: user.ID,
		Destination: "https://destination.example.net/landing", State: domain.CodeActive,
		Styling: domain.Styling{
			ID: domain.NewID("sty"), FgColor: "#000000", BgColor: "#ffffff", ModuleShape: domain.ShapeSquare,
			MarginModules: 4, SizePx: 256, ECLevel: domain.ECMedium, ECLevelEffective: domain.ECMedium,
		},
		BlobKey: "codes/" + shortCode + ".png", BlobETag: `"` + shortCode + `"`,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := stall.CreateCode(ctx, code); err != nil {
		t.Fatalf("CreateCode: %v", err)
	}

	metrics := observability.NewMetrics()
	// BatchSize < BufferSize so a worker's first batch fills quickly and wedges in the
	// stalled store; FlushTimeout longer than the run so it stays wedged throughout.
	recorder := analytics.NewRecorder(stall, analytics.Options{
		BufferSize: stBuffer, BatchSize: 20, FlushInterval: 10 * time.Millisecond, FlushTimeout: time.Minute,
		// Wedged workers log a warning when cleanup releases them; keep that out of -v output.
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, metrics.ScanEventsDropped)

	classifier := analytics.NewClassifier()
	codeSvc := codes.NewService(stall, nil, nil, codes.NewCache(), codes.Config{BaseURL: "http://qurator.test"})
	handler := public.NewPublicHandler(public.Options{
		Resolver: codeSvc,
		Recorder: recorder,
		Classify: func(ua string) (string, domain.DeviceCategory, bool) {
			c := classifier.Classify(ua)
			return c.UAFamily, c.DeviceCategory, c.IsBot
		},
	})
	router := httpapi.NewRouter(httpapi.Handlers{Public: handler}, httpapi.Options{
		PerRoute: []httpapi.Middleware{metrics.Middleware},
	})

	// Load: stGoroutines workers share stRequests requests, timing each individually.
	perG := stRequests / stGoroutines
	latencies := make([][]time.Duration, stGoroutines)
	var badStatus atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	for g := 0; g < stGoroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			lat := make([]time.Duration, 0, perG)
			for i := 0; i < perG; i++ {
				req := httptest.NewRequest(http.MethodGet, "/r/"+shortCode, nil)
				req.RemoteAddr = fmt.Sprintf("203.0.113.%d:%d", g, 10000+i)
				req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14) Chrome/120.0.0.0 Mobile Safari/537.36")
				req.Header.Set("Referer", "https://ref.example.org/page")
				rec := httptest.NewRecorder()
				t0 := time.Now()
				router.ServeHTTP(rec, req)
				lat = append(lat, time.Since(t0))
				if rec.Code != http.StatusFound {
					badStatus.Add(1)
				}
			}
			latencies[g] = lat
		}(g)
	}
	wg.Wait()
	wall := time.Since(start)

	if n := badStatus.Load(); n != 0 {
		t.Fatalf("%d of %d redirects did not answer 302", n, stRequests)
	}

	all := make([]time.Duration, 0, stRequests)
	for _, l := range latencies {
		all = append(all, l...)
	}
	if len(all) != stRequests {
		t.Fatalf("recorded %d latencies, want %d", len(all), stRequests)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	p50 := all[len(all)/2]
	p99 := all[len(all)*99/100]
	max := all[len(all)-1]

	// SC-003 budget is 50ms at p99. Under -race every atomic, channel op and mutex is
	// instrumented and the whole process runs several times slower, so the budget is
	// relaxed to 150ms there; the assertion is about shape (flat, not stalled), not speed.
	budget := 50 * time.Millisecond
	if stRaceEnabled() {
		budget = 150 * time.Millisecond
	}
	t.Logf("%d requests / %d goroutines in %v: p50=%v p99=%v max=%v (budget %v)", stRequests, stGoroutines, wall, p50, p99, max, budget)
	if p99 > budget {
		t.Errorf("p99 redirect latency %v exceeds %v with analytics stalled (Principle IV)", p99, budget)
	}

	// SC-005: drops are counted, and the operator can see them on /metrics.
	dropped := stDroppedFromExposition(t, metrics)
	t.Logf("qurator_scan_events_dropped_total = %v; stalled batches = %d", dropped, stall.batches.Load())
	if dropped <= 0 {
		t.Errorf("qurator_scan_events_dropped_total = %v, want > 0 with the store stalled and a %d-event buffer", dropped, stBuffer)
	}
	if stall.batches.Load() == 0 {
		t.Errorf("stalled store never received a batch; the recorder did not exercise the blocked path")
	}

	// FR-017: the cache serves the hot path. Only each goroutine's first cold miss may
	// reach the store, so the bound is comfortably below 100.
	lookups := stall.lookups.Load()
	t.Logf("store GetCodeByShortCode lookups over the run = %d", lookups)
	if lookups >= 100 {
		t.Errorf("store lookups = %d, want < 100 (cache must absorb repeated scans)", lookups)
	}

	// Shutdown: Close is bounded by its context even though every worker is wedged, and
	// what could not be persisted is reported rather than silently lost.
	closeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	err := recorder.Close(closeCtx)
	took := time.Since(t0)
	t.Logf("recorder.Close(500ms) returned %v after %v; Unflushed() = %d", err, took, recorder.Unflushed())
	if took > time.Second {
		t.Errorf("recorder.Close took %v, want ~500ms bound honoured (< 1s)", took)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("recorder.Close = %v, want context.DeadlineExceeded while workers are wedged", err)
	}
	if u := recorder.Unflushed(); u <= 0 {
		t.Errorf("recorder.Unflushed() = %d after a timed-out Close, want > 0", u)
	}
}
