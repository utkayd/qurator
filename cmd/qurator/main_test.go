package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func noEnv(string) (string, bool) { return "", false }

// F11: an unknown first non-flag argument must be rejected as a usage error (exit 2)
// instead of silently starting the server.
func TestUnknownSubcommandIsRejected(t *testing.T) {
	for _, args := range [][]string{
		{"frobnicate", "--out", "x"},
		{"--server-listen=:8080", "frobnicate"},
		{"--server-listen", ":8080", "frobnicate"},
		{"--", "frobnicate"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out bytes.Buffer
			err := run(context.Background(), args, noEnv, &out)
			if err == nil {
				t.Fatal("unknown subcommand must fail")
			}
			if !errors.Is(err, errUsage) {
				t.Fatalf("error must be a usage error (exit code 2), got %v", err)
			}
			if !strings.Contains(err.Error(), "frobnicate") {
				t.Fatalf("error must name the offending argument, got %q", err)
			}
			for _, sub := range []string{"export", "import", "healthcheck"} {
				if !strings.Contains(err.Error(), sub) {
					t.Fatalf("error must list the known subcommand %q, got %q", sub, err)
				}
			}
			if exitCode(err) != 2 {
				t.Fatalf("exit code = %d, want 2", exitCode(err))
			}
		})
	}
	if exitCode(errors.New("boom")) != 1 {
		t.Fatal("ordinary errors must keep exit code 1")
	}
}

// F11: --help lists the subcommands above the flag list and exits 0.
func TestHelpListsSubcommands(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		var out bytes.Buffer
		if err := run(context.Background(), []string{flag}, noEnv, &out); err != nil {
			t.Fatalf("%s: %v", flag, err)
		}
		help := out.String()
		flags := strings.Index(help, "--config")
		if flags < 0 {
			t.Fatalf("%s: flag list missing:\n%s", flag, help)
		}
		for _, sub := range []string{"export", "import", "healthcheck"} {
			i := strings.Index(help, sub)
			if i < 0 || i > flags {
				t.Fatalf("%s: subcommand %q must appear above the flag list:\n%s", flag, sub, help)
			}
		}
	}
}
