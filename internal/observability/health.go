package observability

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Pinger is a minimal readiness check contract. Store and BlobStore
// implementations satisfy it with a Ping(ctx) error method.
type Pinger interface {
	Ping(context.Context) error
}

// healthResponse is the JSON body shared by /healthz and /readyz.
type healthResponse struct {
	Status string          `json:"status"`
	Checks map[string]bool `json:"checks,omitempty"`
}

// Healthz returns a liveness handler that always responds 200 with
// {"status":"ok"} and never calls any dependency. Liveness must not depend on
// downstream services: if the process itself is running, it is alive.
func Healthz() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeHealthJSON(w, http.StatusOK, healthResponse{Status: "ok"})
	})
}

// Readyz returns a readiness handler that pings every dependency in deps,
// each bounded by timeout. It responds 200 with {"status":"ok","checks":{...}}
// when every ping succeeds, or 503 with the failing dependency names (never
// the underlying error text, which may contain sensitive details such as a
// DSN) otherwise.
func Readyz(deps map[string]Pinger, timeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		checks := make(map[string]bool, len(deps))
		var mu sync.Mutex
		var wg sync.WaitGroup

		allOK := true
		for name, p := range deps {
			wg.Add(1)
			go func(name string, p Pinger) {
				defer wg.Done()
				err := p.Ping(ctx)
				mu.Lock()
				defer mu.Unlock()
				checks[name] = err == nil
				if err != nil {
					allOK = false
				}
			}(name, p)
		}
		wg.Wait()

		status := http.StatusOK
		body := healthResponse{Status: "ok", Checks: checks}
		if !allOK {
			status = http.StatusServiceUnavailable
			body.Status = "unavailable"
		}
		writeHealthJSON(w, status, body)
	})
}

func writeHealthJSON(w http.ResponseWriter, status int, body healthResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
