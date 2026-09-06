package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
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
