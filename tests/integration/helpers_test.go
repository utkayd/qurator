package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Shared helpers for the integration package. Everything here is prefixed `it` so it
// cannot collide with helpers other test files in this package define.

const (
	itAdminEmail    = "admin@example.com"
	itAdminPassword = "correct horse battery staple"
	itSessionCookie = "qurator_session"
	itCSRFHeader    = "X-Qurator-Requested-With"

	// itWantCacheControl is the exact redirect/landing Cache-Control pinned by
	// tests/contract/redirect_test.go; the real binary must emit the same bytes.
	itWantCacheControl = "no-store, no-cache, must-revalidate"
)

// itBinPath is the qurator binary built once per package by TestMain.
var itBinPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "qurator-it-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration: mkdtemp:", err)
		os.Exit(1)
	}
	itBinPath = filepath.Join(dir, "qurator")
	build := exec.Command("go", "build", "-trimpath", "-o", itBinPath, "./cmd/qurator")
	build.Dir = itRepoRoot()
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "integration: go build ./cmd/qurator: %v\n%s", err, out)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// itRepoRoot locates the module root from this file's compiled-in path
// (tests/integration/helpers_test.go → ../..).
func itRepoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("integration: runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// itFreeAddr reserves and releases a loopback port so the child can bind it.
func itFreeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// itSafeBuf is a mutex-guarded bytes.Buffer used to capture child stderr while the
// test reads it concurrently.
type itSafeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *itSafeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *itSafeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// itProc is a running qurator child process.
type itProc struct {
	cmd    *exec.Cmd
	Dir    string
	Addr   string // host:port the server listens on
	Base   string // "http://" + Addr
	Stderr *itSafeBuf

	waitErr error
	done    chan struct{}
}

// itEnv builds the hermetic environment for a child: only PATH and HOME are inherited
// (HOME pointed at the work dir), plus whatever the caller sets.
func itEnv(dir string, extra map[string]string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// itRun executes the binary with the given env in dir and waits for it to exit
// (or kills it at the deadline). It returns the exit code and captured stderr.
func itRun(t *testing.T, dir string, extra map[string]string, deadline time.Duration, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(itBinPath, args...)
	cmd.Dir = dir
	cmd.Env = itEnv(dir, extra)
	var stderr itSafeBuf
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", itBinPath, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return itExitCode(err), stderr.String()
	case <-time.After(deadline):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("binary still running after %s; stderr:\n%s", deadline, stderr.String())
		return -1, ""
	}
}

// itStart launches the binary in dir with QURATOR_SERVER_LISTEN set to a free port
// and the given extra env, then blocks until /healthz answers 200 (or the process
// exits / 15s elapse). The process is killed in t.Cleanup if still alive.
func itStart(t *testing.T, dir string, extra map[string]string) *itProc {
	t.Helper()
	return itStartFn(t, dir, func(string) map[string]string { return extra })
}

// itStartWithBase is itStart with QURATOR_SERVER_BASE_URL pointed at the chosen
// listen address, which persisted codes need to mint scan URLs.
func itStartWithBase(t *testing.T, dir string, extra map[string]string) *itProc {
	t.Helper()
	return itStartFn(t, dir, func(addr string) map[string]string {
		env := map[string]string{"QURATOR_SERVER_BASE_URL": "http://" + addr}
		for k, v := range extra {
			env[k] = v
		}
		return env
	})
}

// itDevAdminEnv is the env every authenticated scenario needs: dev mode (no signing
// secret) plus the bootstrap admin itSignin uses.
func itDevAdminEnv() map[string]string {
	return map[string]string{
		"QURATOR_AUTH_DEV_MODE":           "true",
		"QURATOR_AUTH_BOOTSTRAP_EMAIL":    itAdminEmail,
		"QURATOR_AUTH_BOOTSTRAP_PASSWORD": itAdminPassword,
		"QURATOR_LOG_LEVEL":               "warn",
	}
}

// itStartFn is the shared body of itStart/itStartWithBase: extraFn receives the
// reserved listen address so callers can derive address-dependent settings.
func itStartFn(t *testing.T, dir string, extraFn func(addr string) map[string]string) *itProc {
	t.Helper()
	addr := itFreeAddr(t)
	env := map[string]string{"QURATOR_SERVER_LISTEN": addr}
	for k, v := range extraFn(addr) {
		env[k] = v
	}
	cmd := exec.Command(itBinPath)
	cmd.Dir = dir
	cmd.Env = itEnv(dir, env)
	stderr := &itSafeBuf{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", itBinPath, err)
	}
	p := &itProc{cmd: cmd, Dir: dir, Addr: addr, Base: "http://" + addr, Stderr: stderr, done: make(chan struct{})}
	go func() {
		p.waitErr = cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = cmd.Process.Kill()
			<-p.done
		}
	})

	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-p.done:
			t.Fatalf("binary exited during startup (%v); stderr:\n%s", p.waitErr, stderr.String())
		default:
		}
		resp, err := client.Get(p.Base + "/healthz")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return p
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("binary did not become healthy within 15s; stderr:\n%s", stderr.String())
	return nil
}

// Signal delivers sig to the child.
func (p *itProc) Signal(t *testing.T, sig syscall.Signal) {
	t.Helper()
	if err := p.cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signal %v: %v", sig, err)
	}
}

// Wait blocks until the child exits or the deadline passes (in which case it is
// killed and the test fails). Returns the exit code.
func (p *itProc) Wait(t *testing.T, deadline time.Duration) int {
	t.Helper()
	select {
	case <-p.done:
		return itExitCode(p.waitErr)
	case <-time.After(deadline):
		_ = p.cmd.Process.Kill()
		<-p.done
		t.Fatalf("binary did not exit within %s; stderr:\n%s", deadline, p.Stderr.String())
		return -1
	}
}

// Exited reports whether the child has already exited.
func (p *itProc) Exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func itExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// itClient never follows redirects (we assert on the 302 itself).
func itClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// itSession drives the HTTP API of one running process, authenticating with a session
// cookie. The cookie is Secure, so Go's cookie jar would refuse to send it over plain
// http; we attach it by hand instead.
type itSession struct {
	t      *testing.T
	base   string
	client *http.Client
	cookie string
}

// itResp is the decoded shape of one API response.
type itResp struct {
	Status  int
	Header  http.Header
	Body    []byte
	JSON    map[string]any // nil unless the body was a JSON object
	ErrCode string         // error.code from the error envelope, if any
}

// itDo issues one request. body != nil is JSON-encoded. Mutating requests carry the
// CSRF header the cookie path requires.
func (s *itSession) itDo(method, path string, body any, hdr map[string]string) itResp {
	s.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			s.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, s.base+path, rd)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (qurator integration test)")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.cookie != "" {
		req.Header.Set("Cookie", itSessionCookie+"="+s.cookie)
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set(itCSRFHeader, "1")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	out := itResp{Status: resp.StatusCode, Header: resp.Header, Body: raw}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") && len(raw) > 0 && raw[0] == '{' {
		if err := json.Unmarshal(raw, &out.JSON); err != nil {
			s.t.Fatalf("%s %s: invalid JSON %q: %v", method, path, raw, err)
		}
		if e, ok := out.JSON["error"].(map[string]any); ok {
			out.ErrCode, _ = e["code"].(string)
		}
	}
	return out
}

// itSignin signs in with the bootstrap admin and returns a cookie-authenticated
// session (plus the sign-in status for callers that record it).
func itSignin(t *testing.T, base, email, password string) (*itSession, itResp) {
	t.Helper()
	s := &itSession{t: t, base: base, client: itClient()}
	r := s.itDo(http.MethodPost, "/v1/auth/signin", map[string]string{"email": email, "password": password}, nil)
	if r.Status != http.StatusOK {
		t.Fatalf("signin: %d %s", r.Status, r.Body)
	}
	// Re-parse Set-Cookie from the raw header (itDo discards the *http.Response).
	for _, line := range r.Header.Values("Set-Cookie") {
		for _, c := range (&http.Response{Header: http.Header{"Set-Cookie": {line}}}).Cookies() {
			if c.Name == itSessionCookie {
				s.cookie = c.Value
			}
		}
	}
	if s.cookie == "" {
		t.Fatalf("signin set no %s cookie: %v", itSessionCookie, r.Header)
	}
	return s, r
}

// itWaitAnalyticsTotal polls GET /v1/codes/{id}/analytics until total >= want or the
// deadline passes (scan events are ingested asynchronously). Returns the final response.
func itWaitAnalyticsTotal(s *itSession, codeID string, want int64, deadline time.Duration) itResp {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	var last itResp
	for {
		last = s.itDo(http.MethodGet, "/v1/codes/"+codeID+"/analytics", nil, nil)
		if last.Status == http.StatusOK {
			if total, _ := last.JSON["total"].(float64); int64(total) >= want {
				return last
			}
		}
		select {
		case <-ctx.Done():
			return last
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// itAnalyticsTotal extracts total from an analytics response.
func itAnalyticsTotal(r itResp) int64 {
	total, _ := r.JSON["total"].(float64)
	return int64(total)
}
