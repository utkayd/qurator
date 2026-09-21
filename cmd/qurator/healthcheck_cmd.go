package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/utkayd/qurator/internal/config"
)

// runHealthcheck probes the local /readyz (or /healthz with --live) and exits 0 on 200.
// It exists so a distroless image, which has no shell or curl, can still carry an
// exec-form HEALTHCHECK: `HEALTHCHECK CMD ["/qurator", "healthcheck"]`.
//
// The listen address comes from the same configuration the server loads (defaults,
// --config/QURATOR_CONFIG YAML, environment, flags) so a container that sets
// server.listen in a config file passes its own healthcheck. Every argument other than
// --live is handed to config.Load.
func runHealthcheck(args []string, lookupEnv func(string) (string, bool), stdout io.Writer) error {
	path := "/readyz"
	var cfgArgs []string
	for _, a := range args {
		if a == "--live" {
			path = "/healthz"
			continue
		}
		cfgArgs = append(cfgArgs, a)
	}
	cfg, err := config.Load(cfgArgs, lookupEnv)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	listen := cfg.Server.Listen
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("healthcheck: parse server.listen %q: %w", listen, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+path, nil) //nolint:gosec // G704: host and port are the operator's own server.listen from config, never request input
	resp, err := http.DefaultClient.Do(req)                                                                     //nolint:gosec // G704: same operator-configured local target as above
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: %s returned %d", path, resp.StatusCode)
	}
	_, _ = fmt.Fprintf(stdout, "ok %s\n", path)
	return nil
}
