// Command qurator is the single-binary QR service. See specs/001-qr-service-baseline/plan.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/utkayd/qurator/internal/analytics"
	"github.com/utkayd/qurator/internal/auth"
	"github.com/utkayd/qurator/internal/blob"
	"github.com/utkayd/qurator/internal/codes"
	"github.com/utkayd/qurator/internal/config"
	"github.com/utkayd/qurator/internal/console"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi"
	"github.com/utkayd/qurator/internal/httpapi/middleware"
	"github.com/utkayd/qurator/internal/httpapi/public"
	v1 "github.com/utkayd/qurator/internal/httpapi/v1"
	"github.com/utkayd/qurator/internal/observability"
	"github.com/utkayd/qurator/internal/store"

	// Driver registration: importing a driver package opts the binary into it.
	_ "github.com/utkayd/qurator/internal/blob/fsblob"
	_ "github.com/utkayd/qurator/internal/blob/s3blob"
	_ "github.com/utkayd/qurator/internal/store/postgres"
	_ "github.com/utkayd/qurator/internal/store/sqlite"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

const (
	shutdownBudget = 15 * time.Second // drain in-flight requests
	flushBudget    = 5 * time.Second  // then flush analytics, separately bounded
)

// Flusher is implemented by the analytics pipeline; a no-op until Stream D lands.
type Flusher interface {
	Close(ctx context.Context) error
}

// renderer returns the QR renderer used for persisted codes. Stream A replaces this
// with internal/qr; until then creating a dynamic code fails loudly rather than
// silently storing an empty image.
func renderer() codes.Renderer { return pendingRenderer{} }

type pendingRenderer struct{}

func (pendingRenderer) Render(context.Context, string, domain.Styling) ([]byte, error) {
	return nil, errors.New("qr renderer not wired (Stream A pending)")
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.LookupEnv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "qurator:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, lookupEnv func(string) (string, bool), stdout *os.File) error {
	for _, a := range args {
		if a == "--version" || a == "-v" {
			fmt.Fprintln(stdout, "qurator", version)
			return nil
		}
	}

	cfg, err := config.Load(args, lookupEnv)
	if err != nil {
		return err
	}
	logger := observability.NewLogger(cfg.Log.Level, cfg.Log.Format, os.Stderr)
	slog.SetDefault(logger)
	slog.Info("starting", "version", version, "db_driver", cfg.DB.Driver, "blob_driver", cfg.Blob.Driver, "dev_mode", cfg.Auth.DevMode)

	st, err := store.Open(ctx, cfg.DB.Driver, cfg.DB.DSN)
	if err != nil {
		return fmt.Errorf("open metadata store: %w", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	bs, err := blob.Open(ctx, cfg.Blob.Driver, blob.Config{
		Path: cfg.Blob.Path, Endpoint: cfg.Blob.S3.Endpoint, Bucket: cfg.Blob.S3.Bucket, Region: cfg.Blob.S3.Region,
		AccessKey: cfg.Blob.S3.AccessKey.Reveal(), SecretKey: cfg.Blob.S3.SecretKey.Reveal(),
		UseSSL: cfg.Blob.S3.UseSSL, PathStyle: cfg.Blob.S3.PathStyle,
	})
	if err != nil {
		return fmt.Errorf("open blob store: %w", err)
	}

	metrics := observability.NewMetrics()

	// Identity (Stream C).
	authn, err := auth.New(st, auth.AuthOptions{
		SigningSecret: cfg.Auth.SigningSecret,
		DevMode:       cfg.Auth.DevMode,
		SessionTTL:    cfg.Auth.SessionTTL,
		ForwardAuth:   cfg.ForwardAuth,
		Logger:        logger,
	}, time.Now)
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	if cfg.Auth.BootstrapEmail != "" {
		created, err := auth.Bootstrap(ctx, st, cfg.Auth.BootstrapEmail, cfg.Auth.BootstrapPassword.Reveal())
		if err != nil {
			return fmt.Errorf("bootstrap admin: %w", err)
		}
		if created {
			slog.Info("bootstrap admin created", "email", cfg.Auth.BootstrapEmail)
		}
	}
	identity := func(r *http.Request) (string, bool) {
		id, ok := auth.IdentityFrom(r.Context())
		return id.UserID, ok
	}

	// Analytics pipeline (Stream D): non-blocking recorder, flushed at shutdown.
	recorder := analytics.NewRecorder(st, analytics.Options{
		BufferSize:    cfg.Analytics.BufferSize,
		BatchSize:     cfg.Analytics.BatchSize,
		FlushInterval: cfg.Analytics.FlushInterval,
		Logger:        logger,
	}, metrics.ScanEventsDropped)
	var flusher Flusher = recorder
	go analytics.NewRetention(st, cfg.Analytics.RetentionDays, 1000).Run(ctx)
	classifier := analytics.NewClassifier()

	// Dynamic codes (Stream B). The renderer arrives with Stream A.
	codeSvc := codes.NewService(st, bs, renderer(), codes.NewCache(), codes.Config{
		BaseURL:        cfg.Server.BaseURL,
		AllowedSchemes: cfg.Codes.AllowedSchemes,
	})

	handlers := httpapi.Handlers{
		Public: public.NewPublicHandler(public.Options{
			Resolver:            codeSvc,
			Blob:                bs,
			Recorder:            recorder,
			FallbackDestination: cfg.Codes.FallbackDestination,
			Classify: func(ua string) (string, domain.DeviceCategory, bool) {
				c := classifier.Classify(ua)
				return c.UAFamily, c.DeviceCategory, c.IsBot
			},
		}),
		QR:        v1.NewQRHandler(),
		Codes:     v1.NewCodesHandler(codeSvc, identity),
		Auth:      v1.NewAuthHandler(authn, st),
		Tokens:    v1.NewTokensHandler(authn, st),
		Admin:     v1.NewAdminHandler(st),
		Analytics: v1.NewAnalyticsHandler(st, identity),
		Export:    v1.NewExportHandler(),
		Console:   console.New(newConsoleDeps(codeSvc, authn, st)),
		Healthz:   observability.Healthz(),
		Readyz:    observability.Readyz(map[string]observability.Pinger{"store": st, "blob": bs}, 2*time.Second),
	}
	router := httpapi.NewRouter(handlers, httpapi.Options{
		Common: []httpapi.Middleware{
			middleware.RequestID,
			middleware.Recover,
		},
		PerRoute: []httpapi.Middleware{
			metrics.Middleware,
			middleware.Logging,
		},
		Auth: authn.Middleware,
		CSRF: middleware.CSRF(auth.Method),
	})

	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	errCh := make(chan error, 2)
	go func() {
		ln, err := net.Listen("tcp", cfg.Server.Listen)
		if err != nil {
			errCh <- fmt.Errorf("listen %s: %w", cfg.Server.Listen, err)
			return
		}
		slog.Info("listening", "addr", ln.Addr().String())
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	var metricsSrv *http.Server
	if cfg.Server.MetricsListen != "" {
		mux := http.NewServeMux()
		mux.Handle("GET /metrics", metrics.Handler())
		metricsSrv = &http.Server{Addr: cfg.Server.MetricsListen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			slog.Info("metrics listening", "addr", cfg.Server.MetricsListen)
			if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("metrics listen: %w", err)
			}
		}()
	}

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	// Shutdown order (research.md §6): drain requests under one budget, THEN flush
	// analytics under a separate budget so a stuck sink cannot block the drain.
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("server shutdown", "err", err)
	}
	if metricsSrv != nil {
		_ = metricsSrv.Shutdown(shutdownCtx)
	}
	flushCtx, cancelFlush := context.WithTimeout(context.Background(), flushBudget)
	defer cancelFlush()
	if err := flusher.Close(flushCtx); err != nil {
		slog.Warn("analytics flush", "err", err)
	}
	slog.Info("stopped")
	return nil
}
