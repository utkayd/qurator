package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// runHealthcheck probes the local /readyz (or /healthz with --live) and exits 0 on 200.
// It exists so a distroless image, which has no shell or curl, can still carry an
// exec-form HEALTHCHECK: `HEALTHCHECK CMD ["/qurator", "healthcheck"]`.
func runHealthcheck(args []string, lookupEnv func(string) (string, bool), stdout io.Writer) error {
	path := "/readyz"
	for _, a := range args {
		if a == "--live" {
			path = "/healthz"
		}
	}
	listen := ":8080"
	if v, ok := lookupEnv("QURATOR_SERVER_LISTEN"); ok && v != "" {
		listen = v
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("healthcheck: parse QURATOR_SERVER_LISTEN %q: %w", listen, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+path, nil)
	resp, err := http.DefaultClient.Do(req)
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
