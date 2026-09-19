package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHealthcheckSubcommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(200)
		case "/readyz":
			w.WriteHeader(503)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	env := func(k string) (string, bool) {
		if k == "QURATOR_SERVER_LISTEN" {
			return "0.0.0.0:" + port, true // container-style bind address must map to loopback
		}
		return "", false
	}
	var out bytes.Buffer
	if err := runHealthcheck([]string{"--live"}, env, &out); err != nil || !strings.Contains(out.String(), "ok /healthz") {
		t.Fatalf("--live: err=%v out=%q", err, out.String())
	}
	if err := runHealthcheck(nil, env, &out); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("readyz 503 must fail the probe, got err=%v", err)
	}
	if err := runHealthcheck(nil, func(string) (string, bool) { return "127.0.0.1:1", true }, &out); err == nil {
		t.Fatal("unreachable server must fail the probe")
	}
}

// F18: the probe must honour server.listen from --config (and QURATOR_CONFIG), not only
// QURATOR_SERVER_LISTEN, so a container configured through YAML passes its own
// HEALTHCHECK.
func TestHealthcheckReadsListenFromConfigFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	cfgPath := filepath.Join(t.TempDir(), "qurator.yaml")
	if err := os.WriteFile(cfgPath, []byte("server:\n  listen: \"0.0.0.0:"+port+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	noEnv := func(string) (string, bool) { return "", false }

	var out bytes.Buffer
	if err := runHealthcheck([]string{"--config", cfgPath}, noEnv, &out); err != nil || !strings.Contains(out.String(), "ok /readyz") {
		t.Fatalf("--config: err=%v out=%q", err, out.String())
	}
	out.Reset()
	if err := runHealthcheck([]string{"--live", "--config=" + cfgPath}, noEnv, &out); err == nil {
		t.Fatal("--live against a server without /healthz must fail, proving --live still selects /healthz")
	}
	envCfg := func(k string) (string, bool) {
		if k == "QURATOR_CONFIG" {
			return cfgPath, true
		}
		return "", false
	}
	out.Reset()
	if err := runHealthcheck(nil, envCfg, &out); err != nil || !strings.Contains(out.String(), "ok /readyz") {
		t.Fatalf("QURATOR_CONFIG: err=%v out=%q", err, out.String())
	}
	// Env still overrides the file, matching the server's precedence.
	envBoth := func(k string) (string, bool) {
		switch k {
		case "QURATOR_CONFIG":
			return cfgPath, true
		case "QURATOR_SERVER_LISTEN":
			return "127.0.0.1:1", true
		}
		return "", false
	}
	if err := runHealthcheck(nil, envBoth, &out); err == nil {
		t.Fatal("QURATOR_SERVER_LISTEN must override the config file")
	}
}
