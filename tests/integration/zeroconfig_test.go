package integration

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// T108 / quickstart Scenario 1 / SC-001, SC-002 / FR-040: the binary exec'd with an
// EMPTY environment in a fresh directory must start and serve, generating its own
// credential signing secret into ./data/signing.key (0600) and reusing it on every
// later start so sessions survive restarts. With nothing but dev mode switched on it
// must likewise serve a working instance on SQLite + the local filesystem.

// TestZeroConfig_EmptyEnvStartsAndPersistsSecret: no env at all → starts, /healthz
// is 200, data/signing.key exists with mode 0600. Restarting on the same directory
// reuses the key byte-for-byte, and a session issued before a restart still verifies
// after it.
func TestZeroConfig_EmptyEnvStartsAndPersistsSecret(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "data", "signing.key")
	client := itClient()

	// 1. Completely empty environment (only the listen port, so the test can reach it).
	started := time.Now()
	p := itStart(t, dir, nil)
	if took := time.Since(started); took > 5*time.Second {
		t.Fatalf("took %s to become healthy, want < 5s (SC-002)", took)
	}
	resp, err := client.Get(p.Base + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz: %d", resp.StatusCode)
	}

	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("signing key not persisted at %s: %v", keyPath, err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("%s mode = %o, want 0600", keyPath, perm)
	}
	key1, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(string(key1))) == 0 {
		t.Fatal("signing key file is empty")
	}
	if !strings.Contains(p.Stderr.String(), "generated signing secret") {
		t.Errorf("first start did not log key generation; stderr:\n%s", p.Stderr.String())
	}
	if strings.Contains(p.Stderr.String(), strings.TrimSpace(string(key1))) {
		t.Fatal("the generated secret value appeared in the log output")
	}
	p.Signal(t, syscall.SIGTERM)
	if code := p.Wait(t, 20*time.Second); code != 0 {
		t.Fatalf("exit code %d after SIGTERM, want 0; stderr:\n%s", code, p.Stderr.String())
	}

	// 2. Restart with a bootstrap admin (still no secret, no dev mode): the key must be
	// reused, not regenerated, and a session can be issued against it.
	env := map[string]string{
		"QURATOR_AUTH_BOOTSTRAP_EMAIL":    itAdminEmail,
		"QURATOR_AUTH_BOOTSTRAP_PASSWORD": itAdminPassword,
	}
	p = itStart(t, dir, env)
	key2, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(key1) != string(key2) {
		t.Fatal("signing key changed across a restart on the same data dir")
	}
	if strings.Contains(p.Stderr.String(), "generated signing secret") {
		t.Errorf("second start regenerated the signing secret; stderr:\n%s", p.Stderr.String())
	}
	sess, _ := itSignin(t, p.Base, itAdminEmail, itAdminPassword)
	if r := sess.itDo(http.MethodGet, "/v1/auth/me", nil, nil); r.Status != http.StatusOK {
		t.Fatalf("/v1/auth/me before restart: %d %s", r.Status, r.Body)
	}
	p.Signal(t, syscall.SIGTERM)
	if code := p.Wait(t, 20*time.Second); code != 0 {
		t.Fatalf("exit code %d after SIGTERM, want 0; stderr:\n%s", code, p.Stderr.String())
	}

	// 3. Restart again: the cookie minted by the previous process must still verify.
	p = itStart(t, dir, env)
	key3, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(key1) != string(key3) {
		t.Fatal("signing key changed across the second restart")
	}
	sess.base = p.Base
	if r := sess.itDo(http.MethodGet, "/v1/auth/me", nil, nil); r.Status != http.StatusOK {
		t.Fatalf("/v1/auth/me after restart with the old cookie: %d %s (session did not survive)", r.Status, r.Body)
	}
	p.Signal(t, syscall.SIGTERM)
	if code := p.Wait(t, 20*time.Second); code != 0 {
		t.Fatalf("exit code %d after SIGTERM, want 0; stderr:\n%s", code, p.Stderr.String())
	}
}

// TestZeroConfig_RefusesWhenSecretCannotBePersisted: no secret, no dev mode, and a
// data dir that cannot be written → non-zero exit, and the error names
// QURATOR_AUTH_SIGNING_SECRET as the way out (FR-040).
func TestZeroConfig_RefusesWhenSecretCannotBePersisted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	ro := filepath.Join(dir, "readonly")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })

	code, stderr := itRun(t, dir, map[string]string{"QURATOR_SERVER_DATA_DIR": filepath.Join(ro, "data")}, 5*time.Second)
	if code == 0 {
		t.Fatalf("binary started although the signing secret could not be persisted (FR-040)")
	}
	if !strings.Contains(stderr, "QURATOR_AUTH_SIGNING_SECRET") {
		t.Fatalf("stderr does not name QURATOR_AUTH_SIGNING_SECRET:\n%s", stderr)
	}
	// Refusing must not leave half-created state behind.
	if _, err := os.Stat(filepath.Join(dir, "data")); !os.IsNotExist(err) {
		t.Fatalf("./data exists after a refused start (stat err=%v)", err)
	}
}

// TestZeroConfig_DevModeServesWithDefaults: dev mode + a listen address, nothing else.
// Every default must be usable: SQLite at ./data/qurator.db, fs blobs, protected
// ephemeral endpoint, public scan route, clean SIGTERM.
func TestZeroConfig_DevModeServesWithDefaults(t *testing.T) {
	dir := t.TempDir()
	started := time.Now()
	p := itStart(t, dir, map[string]string{"QURATOR_AUTH_DEV_MODE": "true"})
	if took := time.Since(started); took > 5*time.Second {
		t.Fatalf("took %s to become healthy, want < 5s (SC-002)", took)
	}

	client := itClient()
	get := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := client.Get(p.Base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp, string(body)
	}

	if resp, body := get("/healthz"); resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz: %d %s", resp.StatusCode, body)
	}
	// Readiness pings the store and blob store, so it proves both defaults opened.
	if resp, body := get("/readyz"); resp.StatusCode != http.StatusOK {
		t.Fatalf("/readyz: %d %s", resp.StatusCode, body)
	}
	// ephemeral.public defaults to false: the ephemeral endpoint is protected.
	if resp, body := get("/v1/qr?content=hi"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/v1/qr unauthenticated: %d %s, want 401", resp.StatusCode, body)
	}
	// The public scan route has no auth middleware at all: unknown code → landing page.
	resp, body := get("/r/nope")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/r/nope: %d %s, want 200 landing", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("/r/nope Content-Type %q, want text/html", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != itWantCacheControl {
		t.Fatalf("/r/nope Cache-Control %q, want %q", cc, itWantCacheControl)
	}

	// SC-002: the SQLite default landed in ./data relative to the working directory.
	dbPath := filepath.Join(dir, "data", "qurator.db")
	if st, err := os.Stat(dbPath); err != nil || st.IsDir() {
		t.Fatalf("default SQLite file %s: err=%v", dbPath, err)
	}

	p.Signal(t, syscall.SIGTERM)
	if code := p.Wait(t, 20*time.Second); code != 0 {
		t.Fatalf("exit code %d after SIGTERM, want 0; stderr:\n%s", code, p.Stderr.String())
	}
}
