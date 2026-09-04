package public_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/utkayd/qurator/internal/blob/blobtest"
	"github.com/utkayd/qurator/internal/codes"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi/public"
	"github.com/utkayd/qurator/internal/store/storetest"
)

// These benchmarks guard the Principle IV scan path (GET /r/{code}). The constitution's
// Development Workflow requires a benchmark on every path named in Principles III and
// IV, and .github/workflows/bench.yml gates pull requests on them with benchstat.
//
// Warm is the steady state: every scan hits the resolver cache and touches no store.
// Cold forces the one permitted store lookup (FR-017) on every iteration by
// invalidating the cache entry first; the Invalidate call itself is inside the timed
// region, so Cold is an upper bound on the miss path rather than a pure measurement.

type benchRenderer struct{}

func (benchRenderer) Render(_ context.Context, content string, s domain.Styling, _ []byte, _ bool) ([]byte, domain.ECLevel, error) {
	return []byte("\x89PNG\r\n\x1a\n" + content), s.ECLevel, nil
}

type benchFixture struct {
	handler http.Handler
	cache   *codes.Cache
	code    string
}

func newBenchFixture(b *testing.B) *benchFixture {
	b.Helper()
	ctx := context.Background()
	st := storetest.NewMemStore()
	bl := blobtest.NewMemBlob()
	cache := codes.NewCache()
	svc := codes.NewService(st, bl, benchRenderer{}, cache, codes.Config{
		BaseURL:        "https://qr.example.test",
		AllowedSchemes: []string{"http", "https"},
	})
	u := &domain.User{ID: domain.NewUserID(), Email: "bench@example.com", Source: domain.UserSourceLocal}
	if err := st.CreateUser(ctx, u); err != nil {
		b.Fatal(err)
	}
	c, err := svc.Create(ctx, codes.CreateInput{UserID: u.ID, Destination: "https://example.com/bench", Alias: "bench-code"})
	if err != nil {
		b.Fatal(err)
	}
	pub := public.NewPublicHandler(public.Options{
		Resolver: svc,
		Blob:     bl,
		Recorder: domain.NopRecorder{},
	})
	// PublicHandler dispatches on r.Pattern, which only a ServeMux sets: mount it exactly
	// as httpapi.NewRouter does for the public group.
	mux := http.NewServeMux()
	mux.Handle("GET /r/{code}", pub)
	return &benchFixture{handler: mux, cache: cache, code: c.ShortCode}
}

func (f *benchFixture) scan(b *testing.B) {
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/r/"+f.code, nil))
	if rec.Code != http.StatusFound {
		b.Fatalf("status %d, want 302", rec.Code)
	}
}

func BenchmarkRedirectWarm(b *testing.B) {
	f := newBenchFixture(b)
	f.scan(b) // populate the cache once
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			f.scan(b)
		}
	})
}

func BenchmarkRedirectCold(b *testing.B) {
	f := newBenchFixture(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f.cache.Invalidate(f.code)
		f.scan(b)
	}
}
