package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/utkayd/qurator/internal/auth"
	"github.com/utkayd/qurator/internal/config"
	"github.com/utkayd/qurator/internal/store/storetest"
)

// captureWarnings runs fn with the default slog handler swapped for a buffer and returns
// what was logged.
func captureWarnings(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	fn()
	return buf.String()
}

// TestWarnIfNobodyCanSignIn covers the operator warning main emits after Bootstrap: an
// empty, unconfigured instance says nobody can sign in, unless forward-auth is enabled, in
// which case the proxy provisions users and only the admin role is missing (F09).
func TestWarnIfNobodyCanSignIn(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name        string
		forwardAuth bool
		bootstrap   bool
		seedUser    bool
		want        string
		wantNot     string
	}{
		{name: "empty instance", want: "nobody can sign in"},
		{name: "forward-auth empty instance", forwardAuth: true, want: "none will be admin", wantNot: "nobody can sign in"},
		{name: "bootstrap credentials configured", bootstrap: true, wantNot: "no users exist"},
		{name: "users already exist", seedUser: true, wantNot: "no users exist"},
		{name: "forward-auth with users", forwardAuth: true, seedUser: true, wantNot: "no users exist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := storetest.NewMemStore()
			if tc.seedUser {
				if _, err := auth.Bootstrap(ctx, st, "root@example.com", "hunter2hunter2hunter2"); err != nil {
					t.Fatal(err)
				}
			}
			cfg := &config.Config{}
			cfg.ForwardAuth.Enabled = tc.forwardAuth
			if tc.bootstrap {
				cfg.Auth.BootstrapEmail = "root@example.com"
				cfg.Auth.BootstrapPassword = config.Secret("hunter2hunter2hunter2")
			}
			out := captureWarnings(t, func() {
				if err := warnIfNobodyCanSignIn(ctx, st, cfg); err != nil {
					t.Fatal(err)
				}
			})
			if tc.want != "" && !strings.Contains(out, tc.want) {
				t.Errorf("log missing %q:\n%s", tc.want, out)
			}
			if tc.wantNot != "" && strings.Contains(out, tc.wantNot) {
				t.Errorf("log must not contain %q:\n%s", tc.wantNot, out)
			}
		})
	}
}
