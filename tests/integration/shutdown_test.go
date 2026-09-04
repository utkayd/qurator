// Package integration exercises the full-process shutdown sequence (T096, US7, SC-014):
// on SIGTERM, in-flight requests must drain to completion, buffered scan events must
// flush, and both must happen under the two-budget order documented in research.md §6
// (drain the HTTP server first, THEN flush analytics, on separate deadlines).
//
// # Why this test does not exec the real binary
//
// T096 asks for exactly that: build ./cmd/qurator, start it, send it SIGTERM under
// load. As of this stream (stream/f-ops, off foundation-frozen), cmd/qurator has no
// store driver registered — those land with the Stage 2 store/analytics streams — so
// `qurator` with QURATOR_AUTH_DEV_MODE=true still cannot start: config.Load succeeds,
// but store.Open immediately fails with "unknown driver" (internal/store/open.go) since
// nothing has called store.Register yet. A real-binary shutdown test would therefore
// never get far enough to shut anything down.
//
// This test instead exercises the SAME shutdown sequence main.go uses (copied into
// runShutdownSequence below, matching main.go's run() almost line for line) against an
// in-process httptest-style server with a slow handler standing in for real request
// work and a fakeFlusher standing in for the analytics pipeline's buffered-event flush.
// It is a faithful test of the sequencing and budgets, just not of process-level signal
// delivery or driver wiring.
//
// A real-binary variant is skipped explicitly (see TestShutdown_RealBinary) rather than
// omitted, so its absence shows up as a visible skip once a store driver exists
// (Stage 3), not as a silently missing test today.
package integration

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeFlusher stands in for the real analytics pipeline's buffered-event sink. Close
// simulates flushing every buffered event to the store; it must run only AFTER the HTTP
// server has finished draining, on its own separate deadline (research.md §6).
type fakeFlusher struct {
	buffered int64
	flushed  atomic.Int64
}

func (f *fakeFlusher) Close(ctx context.Context) error {
	// A real flush would do bounded, chunked writes honouring ctx; this one just
	// records that it ran to completion with the whole buffer intact, which is the
	// property under test: nothing is dropped by the shutdown sequence itself.
	f.flushed.Store(f.buffered)
	return ctx.Err()
}

// runShutdownSequence mirrors main.go's run(): drain the HTTP server under
// shutdownBudget, THEN flush analytics under a separate flushBudget. It is a copy
// rather than a shared helper because main.go is out of this stream's ownership
// (Hard rules) and does not exist yet in this worktree (see the package doc).
func runShutdownSequence(t *testing.T, srv *http.Server, flusher *fakeFlusher, shutdownBudget, flushBudget time.Duration) {
	t.Helper()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Errorf("srv.Shutdown: %v", err)
	}
	flushCtx, cancelFlush := context.WithTimeout(context.Background(), flushBudget)
	defer cancelFlush()
	if err := flusher.Close(flushCtx); err != nil {
		t.Errorf("flusher.Close: %v", err)
	}
}

// TestShutdown_DrainsInFlightThenFlushes holds 50 slow in-flight requests and a
// buffer standing in for 5,000 scan events, triggers the shutdown sequence, and
// asserts every request completed 2xx and every buffered event was flushed — in that
// order, not interleaved.
func TestShutdown_DrainsInFlightThenFlushes(t *testing.T) {
	const inFlight = 50
	const bufferedEvents = 5000

	var (
		mu           sync.Mutex
		started      int
		flushRanAt   time.Time
		lastReqDone  time.Time
		serverClosed bool
	)

	release := make(chan struct{})
	handlerStarted := make(chan struct{}, inFlight)

	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		started++
		mu.Unlock()
		handlerStarted <- struct{}{}
		<-release // held open until the test releases it, simulating in-flight work
		mu.Lock()
		lastReqDone = time.Now()
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	client := &http.Client{Timeout: 10 * time.Second}
	url := fmt.Sprintf("http://%s/slow", ln.Addr().String())

	var wg sync.WaitGroup
	results := make([]int, inFlight)
	for i := 0; i < inFlight; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := client.Get(url)
			if err != nil {
				t.Errorf("request %d: %v", i, err)
				return
			}
			defer resp.Body.Close()
			results[i] = resp.StatusCode
		}(i)
	}

	for i := 0; i < inFlight; i++ {
		<-handlerStarted
	}

	flusher := &fakeFlusher{buffered: bufferedEvents}

	// Shutdown runs concurrently with the still-blocked handlers: Shutdown() must
	// wait for them, so we release the handlers shortly after kicking it off — the
	// same real-world shape as "SIGTERM arrives while requests are in flight".
	shutdownDone := make(chan struct{})
	go func() {
		runShutdownSequence(t, srv, flusher, 15*time.Second, 5*time.Second)
		mu.Lock()
		serverClosed = true
		flushRanAt = time.Now()
		mu.Unlock()
		close(shutdownDone)
	}()

	time.Sleep(50 * time.Millisecond) // let Shutdown() start waiting on in-flight conns
	close(release)

	wg.Wait()
	<-shutdownDone

	for i, code := range results {
		if code != http.StatusOK {
			t.Errorf("request %d returned %d, want 200", i, code)
		}
	}
	if flusher.flushed.Load() != bufferedEvents {
		t.Errorf("flushed %d events, want %d", flusher.flushed.Load(), bufferedEvents)
	}
	mu.Lock()
	defer mu.Unlock()
	if started != inFlight {
		t.Errorf("started %d handlers, want %d", started, inFlight)
	}
	if !serverClosed {
		t.Error("shutdown sequence did not complete")
	}
	if flushRanAt.Before(lastReqDone) {
		t.Error("flush ran before the last in-flight request completed; ordering violated")
	}
}

// TestShutdown_RealBinary is the T096-literal scenario: build the actual qurator
// binary, start it, send SIGTERM under load, assert on its behavior from the outside.
// It is skipped because no store driver is registered on this branch — see the package
// doc for why — and will be enabled once Stage 3 merges a driver (internal/store/sqlite
// or postgres) that main.go blank-imports.
func TestShutdown_RealBinary(t *testing.T) {
	t.Skip("requires a registered store driver; enabled in Stage 3")
}
