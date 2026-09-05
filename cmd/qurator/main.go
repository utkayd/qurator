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
	"path/filepath"
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
	"github.com/utkayd/qurator/internal/qr"
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

// codesRenderer adapts internal/qr to the narrow codes.Renderer interface: the
// persisted PNG for a dynamic code, rendered with the code's stored styling.
type codesRenderer struct{ r *qr.Renderer }

func (c codesRenderer) Render(ctx context.Context, content string, s domain.Styling, logo []byte, autoRaise bool) ([]byte, domain.ECLevel, error) {
	opts := qr.Options{
		Content: []byte(content),
		Format:  qr.FormatPNG,
		FgColor: s.FgColor,
		BgColor: s.BgColor,
		Shape:   qr.ModuleShape(s.ModuleShape),
		Margin:  s.MarginModules,
		SizePx:  s.SizePx,
		ECLevel: qr.ECLevel(s.ECLevel),
	}
	if logo != nil {
		opts.Logo = &qr.Logo{Image: logo, Scale: s.LogoScale, AutoRaise: autoRaise}
	}
	res, err := c.r.Render(ctx, opts)
	if err != nil {
		return nil, "", err
	}
	return res.Bytes, domain.ECLevel(res.ECLevelEffective), nil
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
			_, err := fmt.Fprintln(stdout, "qurator", version)
			return err
		}
	}
	if len(args) > 0 {
		switch args[0] {
		case "export":
			return runExport(ctx, args[1:], lookupEnv, stdout)
		case "import":
			return runImport(ctx, args[1:], lookupEnv, stdout)
		}
	}

	cfg, err := config.Load(args, lookupEnv)
	if err != nil {
		return err
	}
	logger := observability.NewLogger(cfg.Log.Level, cfg.Log.Format, os.Stderr)
	slog.SetDefault(logger)
	slog.Info("starting", "version", version, "db_driver", cfg.DB.Driver, "blob_driver", cfg.Blob.Driver, "dev_mode", cfg.Auth.DevMode)

	// FR-040: with no configured signing secret and dev mode off, generate one on
	// first start and persist it under the data dir so sessions survive restarts.
	// Refuse to start only if that file can neither be read nor created. Dev mode
	// keeps its ephemeral key (auth.New). The value is never logged.
	if !cfg.Auth.SigningSecret.IsSet() && !cfg.Auth.DevMode {
		keyPath := filepath.Join(cfg.Server.DataDir, auth.SigningKeyFile)
		secret, created, err := auth.LoadOrCreateSigningSecret(keyPath)
		if err != nil {
			return err
		}
		cfg.Auth.SigningSecret = secret
		if created {
			slog.Info("generated signing secret", "path", keyPath)
		} else if auth.SigningSecretFilePermissive(keyPath) {
			slog.Warn("signing secret file is readable by group or others; chmod 0600 it", "path", keyPath)
		}
	}

	st, err := store.Open(ctx, cfg.DB.Driver, cfg.DB.DSN)
	if err != nil {
		return fmt.Errorf("open metadata store: %w", err)
	}
	defer func() { _ = st.Close() }()
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

	// QR rendering (Stream A): shared by the ephemeral endpoint and persisted codes.
	qrRenderer := qr.NewRenderer(qr.Bounds{
		MaxPx:       cfg.Render.MaxPx,
		MaxDuration: cfg.Render.MaxDuration,
		MaxPayload:  cfg.Render.MaxPayloadBytes,
	})
	ephemeralLimiter := middleware.NewRateLimiter(cfg.Ephemeral.RateLimitPerMinute).Middleware
	isAdmin := func(r *http.Request) bool {
		id, ok := auth.IdentityFrom(r.Context())
		return ok && id.IsAdmin
	}

	// Dynamic codes (Stream B).
	codeSvc := codes.NewService(st, bs, codesRenderer{qrRenderer}, codes.NewCache(), codes.Config{
		BaseURL:        cfg.Server.BaseURL,
		AllowedSchemes: cfg.Codes.AllowedSchemes,
		URLMode:        cfg.Images.URLMode,
		PublicBaseURL:  cfg.Images.PublicBaseURL,
		PresignTTL:     cfg.Images.PresignTTL,
		BatchMax:       cfg.Codes.BatchMax,
		BatchWorkers:   cfg.Codes.BatchWorkers,
	})

	handlers := httpapi.Handlers{
		Public: public.NewPublicHandler(public.Options{
			Resolver:            codeSvc,
			Blob:                bs,
			Recorder:            recorder,
			FallbackDestination: cfg.Codes.FallbackDestination,
			ImagesDisabled:      !cfg.Images.ServeViaInstance,
			Classify: func(ua string) (string, domain.DeviceCategory, bool) {
				c := classifier.Classify(ua)
				return c.UAFamily, c.DeviceCategory, c.IsBot
			},
		}),
		QR:        v1.NewQRHandler(qrRenderer, cfg.Ephemeral, auth.IsAuthenticated, ephemeralLimiter),
		Codes:     v1.NewCodesHandler(codeSvc, identity),
		Auth:      v1.NewAuthHandler(authn, st),
		Tokens:    v1.NewTokensHandler(authn, st),
		Admin:     v1.NewAdminHandler(st),
		Analytics: v1.NewAnalyticsHandler(st, identity),
		Export:    v1.NewExportHandler(st, isAdmin),
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
		// 10 sign-in attempts per minute per TCP peer. Behind a reverse proxy every
		// request shares the proxy's peer address, so this is effectively a global
		// limit: qurator never trusts X-Forwarded-For (research.md §2).
		SigninLimiter: middleware.NewRateLimiter(10).Middleware,
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
