package middleware

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/utkayd/qurator/internal/httpapi"
)

// RateLimiter is a per-client token bucket. The client key is the TCP peer address used
// TRANSIENTLY: it lives in memory for the bucket's lifetime and is never logged or
// persisted (FR-022). Buckets idle longer than 10 minutes are evicted.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // tokens per second
	burst   float64
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter allows perMinute requests sustained with a burst of the same size.
func NewRateLimiter(perMinute int) *RateLimiter {
	rl := &RateLimiter{
		buckets: map[string]*bucket{},
		rate:    float64(perMinute) / 60,
		burst:   float64(perMinute),
		now:     time.Now,
	}
	go rl.evictLoop()
	return rl
}

func (rl *RateLimiter) allow(key string) (bool, time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := rl.now()
	b, ok := rl.buckets[key]
	if !ok {
		b = &bucket{tokens: rl.burst, last: now}
		rl.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * rl.rate
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1-b.tokens)/rl.rate*float64(time.Second)) + time.Second
}

func (rl *RateLimiter) evictLoop() {
	for range time.Tick(time.Minute) {
		cutoff := rl.now().Add(-10 * time.Minute)
		rl.mu.Lock()
		for k, b := range rl.buckets {
			if b.last.Before(cutoff) {
				delete(rl.buckets, k)
			}
		}
		rl.mu.Unlock()
	}
}

// Middleware applies the limiter keyed by the TCP peer. It does not read
// X-Forwarded-For: an attacker controls that header, so keying on it would let them
// escape the limit by rotating it.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ok, retry := rl.allow(host)
		if !ok {
			secs := int(retry.Seconds())
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			httpapi.WriteError(w, httpapi.CodeRateLimited, "Too many requests.", map[string]any{"retry_after_s": secs})
			return
		}
		next.ServeHTTP(w, r)
	})
}
