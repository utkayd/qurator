package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
}

func TestRequestIDIgnoresClientValue(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-Id", "attacker-chosen")
	RequestID(okHandler()).ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Request-Id"); got == "" || got == "attacker-chosen" {
		t.Fatalf("request id %q", got)
	}
}

func TestRecoverHidesStack(t *testing.T) {
	h := Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("boom: /etc/secret") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "boom") || strings.Contains(rec.Body.String(), "goroutine") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestCSRF(t *testing.T) {
	cookie := func(*http.Request) string { return "cookie" }
	bearer := func(*http.Request) string { return "bearer" }
	cases := []struct {
		name   string
		method string
		auth   AuthMethodFunc
		header bool
		want   int
	}{
		{"cookie POST no header", "POST", cookie, false, 403},
		{"cookie POST with header", "POST", cookie, true, 200},
		{"cookie GET no header", "GET", cookie, false, 200},
		{"bearer POST no header", "POST", bearer, false, 200},
		{"anonymous POST", "POST", func(*http.Request) string { return "" }, false, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, "/v1/codes", nil)
			if c.header {
				req.Header.Set(CSRFHeader, "1")
			}
			rec := httptest.NewRecorder()
			CSRF(c.auth)(okHandler()).ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Fatalf("got %d want %d", rec.Code, c.want)
			}
		})
	}
}

func TestRateLimiterKeysOnPeerNotXFF(t *testing.T) {
	rl := &RateLimiter{buckets: map[string]*bucket{}, rate: 0, burst: 2, now: func() time.Time { return time.Unix(0, 0) }}
	h := rl.Middleware(okHandler())
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/v1/qr", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		req.Header.Set("X-Forwarded-For", "203.0.113."+string(rune('1'+i))) // rotating XFF must not help
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		want := 200
		if i == 2 {
			want = 429
		}
		if rec.Code != want {
			t.Fatalf("request %d: got %d want %d", i, rec.Code, want)
		}
		if want == 429 && rec.Header().Get("Retry-After") == "" {
			t.Fatal("missing Retry-After")
		}
	}
}
