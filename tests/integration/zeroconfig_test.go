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

// T108 / quickstart Scenario 1 / SC-002 / FR-040: the binary exec'd with an EMPTY
// environment in a fresh directory. It must refuse to start without a signing secret
// (naming the variable that fixes it), and with nothing but dev mode switched on it
// must serve a working instance on SQLite + the local filesystem, having created its
// own data directory.

// TestZeroConfig_RefusesWithoutSigningSecret: no env at all → non-zero exit, and the
// error names QURATOR_AUTH_SIGNING_SECRET so the operator knows what to set.
func TestZeroConfig_RefusesWithoutSigningSecret(t *testing.T) {
	dir := t.TempDir()
	code, stderr := itRun(t, dir, nil, 5*time.Second)
	if code == 0 {
		t.Fatalf("binary started with no configuration; must refuse without a signing secret (FR-040)")
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
