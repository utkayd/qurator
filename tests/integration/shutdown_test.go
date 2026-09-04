// Package integration exercises the full-process shutdown sequence (T096, US7, SC-014):
// on SIGTERM, in-flight requests must drain to completion, buffered scan events must
// flush, and both must happen under the two-budget order documented in research.md §6
// (drain the HTTP server first, THEN flush analytics, on separate deadlines).
//
// # Two variants
//
// TestShutdown_DrainsInFlightThenFlushes exercises the SAME shutdown sequence main.go
// uses (copied into runShutdownSequence below) against an in-process server with a slow
// handler and a fakeFlusher standing in for the analytics pipeline. It was written on
// stream/f-ops before any store driver existed, and it remains the precise test of
// sequencing and budgets.
//
// TestShutdown_RealBinary (Stage 3, T096-literal, quickstart Scenario 8) builds and
// execs the actual qurator binary, sends SIGTERM while 500 scans are in flight, and
// asserts from the outside: exit 0, no accepted request dropped, and after a restart on
// the same data directory the analytics total equals the number of 302s served.
package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"syscall"
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

// itScanUA is a browser-looking User-Agent so scans classify as ordinary traffic.
const itScanUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0 Safari/537.36"

// itScanOutcome is what one concurrent scanner observed.
type itScanOutcome struct {
	status int
	err    error
}

// TestShutdown_RealBinary is the T096-literal scenario (SC-014): start the real
// binary, fire 500 scans concurrently, SIGTERM it while they are in flight.
//
// Requests the server ACCEPTED must all finish with an HTTP status: a connection reset
// or EOF mid-request is a drain failure. Requests that never reached the server because
// the listener had already closed surface as "connection refused"; those were not sent
// before SIGTERM in any meaningful sense and are tolerated (but counted and logged).
// The number of 302s observed is the number of scan events the store must hold after a
// restart — flush-on-shutdown must lose none of them.
func TestShutdown_RealBinary(t *testing.T) {
	const (
		scans       = 500
		alias       = "drain"
		destination = "https://example.com/drain"
		// SIGTERM is sent once this many scans have completed, so the process is
		// demonstrably serving when the signal lands and the rest are in flight.
		signalAfter = 25
	)
	dir := t.TempDir()
	env := itDevAdminEnv()
	p := itStartWithBase(t, dir, env)

	s, _ := itSignin(t, p.Base, itAdminEmail, itAdminPassword)
	created := s.itDo(http.MethodPost, "/v1/codes", map[string]any{"destination": destination, "alias": alias}, nil)
	if created.Status != http.StatusCreated {
		t.Fatalf("create code: %d %s", created.Status, created.Body)
	}
	codeID, _ := created.JSON["id"].(string)

	// One transport for all scanners; a bounded connection pool keeps the burst within
	// the listen backlog so nothing is dropped by the kernel rather than the server.
	transport := &http.Transport{MaxConnsPerHost: 64, MaxIdleConnsPerHost: 64}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	scanURL := p.Base + "/r/" + alias

	var (
		completed atomic.Int64
		wg        sync.WaitGroup
		outcomes  = make([]itScanOutcome, scans)
		signalled = make(chan struct{})
	)
	for i := 0; i < scans; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, scanURL, nil)
			req.Header.Set("User-Agent", itScanUA)
			resp, err := client.Do(req)
			if err != nil {
				outcomes[i] = itScanOutcome{err: err}
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			outcomes[i] = itScanOutcome{status: resp.StatusCode}
			if completed.Add(1) == signalAfter {
				close(signalled)
			}
		}(i)
	}

	select {
	case <-signalled:
	case <-time.After(20 * time.Second):
		t.Fatalf("fewer than %d scans completed in 20s (%d); stderr:\n%s", signalAfter, completed.Load(), p.Stderr.String())
	}
	p.Signal(t, syscall.SIGTERM)
	wg.Wait()

	if code := p.Wait(t, 25*time.Second); code != 0 {
		t.Fatalf("exit code %d after SIGTERM, want 0; stderr:\n%s", code, p.Stderr.String())
	}

	var served, refused int64
	for i, o := range outcomes {
		switch {
		case o.err == nil && o.status == http.StatusFound:
			served++
		case o.err == nil:
			t.Errorf("scan %d: status %d, want 302", i, o.status)
		case itIsConnRefused(o.err):
			refused++ // never reached the server: the listener was already closed
		default:
			t.Errorf("scan %d: accepted request did not complete cleanly: %v", i, o.err)
		}
	}
	t.Logf("scans: %d served 302, %d refused after listener close", served, refused)
	if served < signalAfter {
		t.Fatalf("only %d scans served, want at least %d", served, signalAfter)
	}

	// Restart on the same data directory: everything flushed at shutdown must be there,
	// and nothing that was never served must be.
	p2 := itStartWithBase(t, dir, env)
	s2, _ := itSignin(t, p2.Base, itAdminEmail, itAdminPassword)
	an := itWaitAnalyticsTotal(s2, codeID, served, 15*time.Second)
	if an.Status != http.StatusOK {
		t.Fatalf("analytics after restart: %d %s", an.Status, an.Body)
	}
	if got := itAnalyticsTotal(an); got != served {
		t.Fatalf("analytics total after restart = %d, want %d (302s served before shutdown); stderr of first run:\n%s", got, served, p.Stderr.String())
	}
	p2.Signal(t, syscall.SIGTERM)
	if code := p2.Wait(t, 20*time.Second); code != 0 {
		t.Fatalf("second instance exit code %d, want 0", code)
	}
}

// itIsConnRefused reports whether err is a dial failure against a closed listener.
func itIsConnRefused(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}
