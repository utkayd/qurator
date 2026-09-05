package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/posflag"
	koanf "github.com/knadh/koanf/v2"
	flag "github.com/spf13/pflag"
)

// envPrefix is stripped from every environment variable before it is
// mapped onto a config key. "QURATOR_SERVER_LISTEN" becomes
// "server.listen": the prefix is dropped, the remainder is lower-cased,
// and every "_" becomes the koanf delimiter "." — so a config key with an
// underscore in its own name is not representable via env (none of ours
// have one). "QURATOR_CONFIG" is reserved: it names the optional YAML
// file, it is never a config key itself.
//
// NOTE on why the env layer below is hand-rolled instead of
// github.com/knadh/koanf/providers/env: that provider's Read() calls
// os.Environ() directly and has no hook for substituting a lookup
// function, so a Load(args, lookupEnv) signature that must stay hermetic
// in tests (no process-env mutation) cannot use it as-is. Instead we walk
// the known set of leaf config keys and resolve each one's env var via
// the injected lookupEnv, which gives identical QURATOR_* precedence
// semantics while staying fully testable.
const envPrefix = "QURATOR_"

// delim is the koanf path delimiter used for every provider and for
// struct nesting (posflag/env "_" is translated to this).
const delim = "."

// ServerConfig controls the public HTTP listener and the optional
// internal metrics listener.
type ServerConfig struct {
	Listen        string `koanf:"listen"`
	BaseURL       string `koanf:"base_url"`
	MetricsListen string `koanf:"metrics_listen"`
	// DataDir is where qurator keeps state it creates for itself and that
	// is not otherwise located by config: today that is the generated
	// credential signing secret (signing.key, FR-040). It is created 0700
	// on first start if missing.
	DataDir string `koanf:"data_dir"`
}

// DBConfig selects and locates the metadata store.
type DBConfig struct {
	Driver string `koanf:"driver"`
	DSN    string `koanf:"dsn"`
}

// S3Config configures the S3-compatible blob driver. It is only consulted
// when Blob.Driver == "s3".
type S3Config struct {
	Endpoint  string `koanf:"endpoint"`
	Bucket    string `koanf:"bucket"`
	Region    string `koanf:"region"`
	AccessKey Secret `koanf:"access_key"`
	SecretKey Secret `koanf:"secret_key"`
	UseSSL    bool   `koanf:"use_ssl"`
	PathStyle bool   `koanf:"path_style"`
}

// BlobConfig selects and locates the blob store.
type BlobConfig struct {
	Driver string   `koanf:"driver"`
	Path   string   `koanf:"path"`
	S3     S3Config `koanf:"s3"`
}

// AuthConfig controls credential signing, dev mode, and the bootstrap
// admin account.
type AuthConfig struct {
	SigningSecret     Secret        `koanf:"signing_secret"`
	DevMode           bool          `koanf:"dev_mode"`
	BootstrapEmail    string        `koanf:"bootstrap_email"`
	BootstrapPassword Secret        `koanf:"bootstrap_password"`
	SessionTTL        time.Duration `koanf:"session_ttl"`
}

// EphemeralConfig controls the unauthenticated, storage-free QR rendering
// path.
type EphemeralConfig struct {
	Public             bool `koanf:"public"`
	RateLimitPerMinute int  `koanf:"rate_limit_per_minute"`
}

// ForwardAuthConfig controls trust of an upstream reverse proxy's identity
// header. Trust is anchored to the TCP peer address, never to header
// content (see research.md §2) — TrustedCIDRs is the allowlist of peers
// permitted to assert Header.
type ForwardAuthConfig struct {
	Enabled      bool     `koanf:"enabled"`
	Header       string   `koanf:"header"`
	TrustedCIDRs []string `koanf:"trusted_cidrs"`
}

// CodesConfig controls short-code creation policy and batch creation limits
// (spec 003, FR-205/FR-207).
type CodesConfig struct {
	AllowedSchemes      []string `koanf:"allowed_schemes"`
	FallbackDestination string   `koanf:"fallback_destination"`
	// BatchMax caps the item count of one POST /v1/codes/batch request.
	BatchMax int `koanf:"batch_max"`
	// BatchWorkers bounds how many batch items render concurrently.
	BatchWorkers int `koanf:"batch_workers"`
}

// ImagesConfig controls how code images are addressed and whether this
// instance serves them at all (spec 003, FR-201).
type ImagesConfig struct {
	// URLMode selects what image_url points at: "instance" (this instance's
	// /i/{id}.png), "public" (PublicBaseURL + "/" + blob key) or "presigned"
	// (a signed S3 link valid for PresignTTL).
	URLMode string `koanf:"url_mode"`
	// PublicBaseURL is the externally reachable root of the bucket or the CDN
	// in front of it. Required for url_mode=public; normalised without a
	// trailing slash by Validate.
	PublicBaseURL string `koanf:"public_base_url"`
	// PresignTTL is the lifetime of presigned links.
	PresignTTL time.Duration `koanf:"presign_ttl"`
	// ServeViaInstance, when false, makes GET /i/{file} answer 404 for every
	// id (FR-204). It cannot be false while url_mode is instance: there would
	// be no way to reach any image.
	ServeViaInstance bool `koanf:"serve_via_instance"`
}

// RenderConfig bounds QR rendering resource usage.
type RenderConfig struct {
	MaxPx           int           `koanf:"max_px"`
	MaxDuration     time.Duration `koanf:"max_duration"`
	MaxPayloadBytes int           `koanf:"max_payload_bytes"`
}

// AnalyticsConfig controls scan-event retention and the async ingest
// pipeline.
type AnalyticsConfig struct {
	RetentionDays int           `koanf:"retention_days"`
	BufferSize    int           `koanf:"buffer_size"`
	BatchSize     int           `koanf:"batch_size"`
	FlushInterval time.Duration `koanf:"flush_interval"`
}

// LogConfig controls the structured logger.
type LogConfig struct {
	Level  string `koanf:"level"`
	Format string `koanf:"format"`
}

// Config is qurator's fully-resolved, validated runtime configuration.
type Config struct {
	Server      ServerConfig      `koanf:"server"`
	DB          DBConfig          `koanf:"db"`
	Blob        BlobConfig        `koanf:"blob"`
	Auth        AuthConfig        `koanf:"auth"`
	Ephemeral   EphemeralConfig   `koanf:"ephemeral"`
	ForwardAuth ForwardAuthConfig `koanf:"forward_auth"`
	Codes       CodesConfig       `koanf:"codes"`
	Images      ImagesConfig      `koanf:"images"`
	Render      RenderConfig      `koanf:"render"`
	Analytics   AnalyticsConfig   `koanf:"analytics"`
	Log         LogConfig         `koanf:"log"`

	// ConfigFile records the YAML file actually loaded (if any), for
	// diagnostics only. Never populated from config itself.
	ConfigFile string `koanf:"-"`
}

// defaults returns the default configuration as a nested map, suitable for
// loading via the confmap provider as the lowest-precedence layer. Every
// exposure-widening field (Ephemeral.Public, ForwardAuth.Enabled,
// Auth.DevMode, Server.MetricsListen) defaults to off/empty (FR-048).
func defaults() map[string]any {
	return map[string]any{
		"server.listen":         ":8080",
		"server.base_url":       "",
		"server.metrics_listen": "",
		"server.data_dir":       "./data",

		"db.driver": "sqlite",
		"db.dsn":    "./data/qurator.db",

		"blob.driver":        "fs",
		"blob.path":          "./data/blobs",
		"blob.s3.endpoint":   "",
		"blob.s3.bucket":     "",
		"blob.s3.region":     "",
		"blob.s3.access_key": "",
		"blob.s3.secret_key": "",
		"blob.s3.use_ssl":    true,
		"blob.s3.path_style": false,

		"auth.signing_secret":     "",
		"auth.dev_mode":           false,
		"auth.bootstrap_email":    "",
		"auth.bootstrap_password": "",
		"auth.session_ttl":        "12h",

		"ephemeral.public":                false,
		"ephemeral.rate_limit_per_minute": 60,

		"forward_auth.enabled":       false,
		"forward_auth.header":        "X-Forwarded-Email",
		"forward_auth.trusted_cidrs": []string{},

		"codes.allowed_schemes":      []string{"http", "https"},
		"codes.fallback_destination": "",
		"codes.batch_max":            500,
		"codes.batch_workers":        4,

		"images.url_mode":           "instance",
		"images.public_base_url":    "",
		"images.presign_ttl":        "1h",
		"images.serve_via_instance": true,

		"render.max_px":            4096,
		"render.max_duration":      "2s",
		"render.max_payload_bytes": 2953,

		"analytics.retention_days": 365,
		"analytics.buffer_size":    10000,
		"analytics.batch_size":     200,
		"analytics.flush_interval": "500ms",

		"log.level":  "info",
		"log.format": "json",
	}
}

// fieldKind describes how to parse a leaf config key's value when it
// arrives as a string from the environment.
type fieldKind int

const (
	kString fieldKind = iota
	kBool
	kInt
	kDuration
	kStringSlice
)

// fieldSpec pairs a koanf leaf path with the kind needed to parse its
// environment string representation into the correctly typed value.
type fieldSpec struct {
	path string
	kind fieldKind
}

// leafFields enumerates every leaf key in Config. It drives both the env
// var name derivation (path "." -> "_", upper-cased, QURATOR_-prefixed)
// and the flag name derivation (path "." -> "-") used by flagSet, so the
// two stay in lockstep by construction.
var leafFields = []fieldSpec{
	{"server.listen", kString},
	{"server.base_url", kString},
	{"server.metrics_listen", kString},
	{"server.data_dir", kString},

	{"db.driver", kString},
	{"db.dsn", kString},

	{"blob.driver", kString},
	{"blob.path", kString},
	{"blob.s3.endpoint", kString},
	{"blob.s3.bucket", kString},
	{"blob.s3.region", kString},
	{"blob.s3.access_key", kString},
	{"blob.s3.secret_key", kString},
	{"blob.s3.use_ssl", kBool},
	{"blob.s3.path_style", kBool},

	{"auth.signing_secret", kString},
	{"auth.dev_mode", kBool},
	{"auth.bootstrap_email", kString},
	{"auth.bootstrap_password", kString},
	{"auth.session_ttl", kDuration},

	{"ephemeral.public", kBool},
	{"ephemeral.rate_limit_per_minute", kInt},

	{"forward_auth.enabled", kBool},
	{"forward_auth.header", kString},
	{"forward_auth.trusted_cidrs", kStringSlice},

	{"codes.allowed_schemes", kStringSlice},
	{"codes.fallback_destination", kString},
	{"codes.batch_max", kInt},
	{"codes.batch_workers", kInt},

	{"images.url_mode", kString},
	{"images.public_base_url", kString},
	{"images.presign_ttl", kDuration},
	{"images.serve_via_instance", kBool},

	{"render.max_px", kInt},
	{"render.max_duration", kDuration},
	{"render.max_payload_bytes", kInt},

	{"analytics.retention_days", kInt},
	{"analytics.buffer_size", kInt},
	{"analytics.batch_size", kInt},
	{"analytics.flush_interval", kDuration},

	{"log.level", kString},
	{"log.format", kString},
}

// envVarName derives "QURATOR_SERVER_LISTEN" from "server.listen".
func envVarName(path string) string {
	return envPrefix + strings.ToUpper(strings.ReplaceAll(path, ".", "_"))
}

// envOverrides resolves every leaf field's env var via lookupEnv and
// returns the flat, typed map[string]any suitable for confmap.Provider.
// Only keys actually present in the environment are included, so this
// layer never shadows the file layer below it with a default.
func envOverrides(lookupEnv func(string) (string, bool)) (map[string]any, error) {
	out := make(map[string]any, len(leafFields))
	for _, f := range leafFields {
		raw, ok := lookupEnv(envVarName(f.path))
		if !ok {
			continue
		}
		val, err := parseFieldValue(f.kind, raw)
		if err != nil {
			return nil, fmt.Errorf("config: %s: %w", envVarName(f.path), err)
		}
		out[f.path] = val
	}
	return out, nil
}

// parseFieldValue converts a raw environment string into the Go value
// type its target field expects.
func parseFieldValue(kind fieldKind, raw string) (any, error) {
	switch kind {
	case kBool:
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid bool %q: %w", raw, err)
		}
		return v, nil
	case kInt:
		v, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid int %q: %w", raw, err)
		}
		return v, nil
	case kDuration:
		v, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid duration %q: %w", raw, err)
		}
		return v, nil
	case kStringSlice:
		if raw == "" {
			return []string{}, nil
		}
		parts := strings.Split(raw, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return parts, nil
	default:
		return raw, nil
	}
}

// flagSet builds the pflag.FlagSet used both as the posflag provider source
// and for parsing --config. One flag is defined per leaf config key, named
// identically to its koanf path with "." replaced by "-"
// (e.g. --server-listen).
func flagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("qurator", flag.ContinueOnError)

	fs.String("config", "", "path to an optional YAML config file")

	fs.String("server-listen", "", "public HTTP listen address")
	fs.String("server-base-url", "", "externally visible base URL")
	fs.String("server-metrics-listen", "", "internal metrics listen address (empty disables /metrics)")
	fs.String("server-data-dir", "", "directory for self-generated state such as signing.key")

	fs.String("db-driver", "", "metadata store driver (sqlite|postgres)")
	fs.String("db-dsn", "", "metadata store DSN")

	fs.String("blob-driver", "", "blob store driver (fs|s3)")
	fs.String("blob-path", "", "filesystem blob store root")
	fs.String("blob-s3-endpoint", "", "S3-compatible endpoint")
	fs.String("blob-s3-bucket", "", "S3 bucket name")
	fs.String("blob-s3-region", "", "S3 region")
	fs.String("blob-s3-access-key", "", "S3 access key")
	fs.String("blob-s3-secret-key", "", "S3 secret key")
	fs.Bool("blob-s3-use-ssl", true, "use TLS when talking to S3")
	fs.Bool("blob-s3-path-style", false, "use path-style S3 addressing")

	fs.String("auth-signing-secret", "", "credential signing secret (generated into <data-dir>/signing.key when empty)")
	fs.Bool("auth-dev-mode", false, "use an ephemeral signing key instead of persisting one")
	fs.String("auth-bootstrap-email", "", "bootstrap admin email")
	fs.String("auth-bootstrap-password", "", "bootstrap admin password")
	fs.Duration("auth-session-ttl", 0, "session lifetime")

	fs.Bool("ephemeral-public", false, "expose the ephemeral render endpoint publicly")
	fs.Int("ephemeral-rate-limit-per-minute", 0, "ephemeral endpoint rate limit per minute")

	fs.Bool("forward-auth-enabled", false, "trust an upstream identity header")
	fs.String("forward-auth-header", "", "upstream identity header name")
	fs.StringSlice("forward-auth-trusted-cidrs", nil, "CIDRs permitted to assert the identity header")

	fs.StringSlice("codes-allowed-schemes", nil, "destination URI schemes permitted at creation")
	fs.String("codes-fallback-destination", "", "destination used when a code has none configured")
	fs.Int("codes-batch-max", 0, "maximum items in one batch create request")
	fs.Int("codes-batch-workers", 0, "concurrent image renders per batch create request")

	fs.String("images-url-mode", "", "how image_url is built (instance|public|presigned)")
	fs.String("images-public-base-url", "", "public root of the bucket or CDN for images.url_mode=public")
	fs.Duration("images-presign-ttl", 0, "lifetime of presigned image links")
	fs.Bool("images-serve-via-instance", true, "serve images at /i/{id}.png on this instance")

	fs.Int("render-max-px", 0, "maximum QR raster dimension in pixels")
	fs.Duration("render-max-duration", 0, "maximum time budget for a single render")
	fs.Int("render-max-payload-bytes", 0, "maximum encoded payload size in bytes")

	fs.Int("analytics-retention-days", 0, "scan event retention in days")
	fs.Int("analytics-buffer-size", 0, "async ingest buffer capacity")
	fs.Int("analytics-batch-size", 0, "async ingest batch insert size")
	fs.Duration("analytics-flush-interval", 0, "async ingest flush interval")

	fs.String("log-level", "", "log level")
	fs.String("log-format", "", "log format (json|text)")

	return fs
}

// Load resolves Config by layering, lowest to highest precedence:
//  1. defaults()
//  2. an optional YAML file, named by --config or QURATOR_CONFIG
//  3. environment variables prefixed QURATOR_ (e.g. QURATOR_SERVER_LISTEN)
//  4. command-line flags (e.g. --server-listen)
//
// lookupEnv is injected (rather than reading os.Getenv directly) so tests
// can supply a hermetic environment instead of mutating the process one.
// Load returns an error if the resulting Config fails Validate.
func Load(args []string, lookupEnv func(string) (string, bool)) (*Config, error) {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}

	k := koanf.New(delim)

	if err := k.Load(confmap.Provider(defaults(), delim), nil); err != nil {
		return nil, fmt.Errorf("config: load defaults: %w", err)
	}

	fs := flagSet()
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("config: parse flags: %w", err)
	}

	configPath, _ := fs.GetString("config")
	if configPath == "" {
		if v, ok := lookupEnv(envPrefix + "CONFIG"); ok {
			configPath = v
		}
	}
	if configPath != "" {
		if err := k.Load(file.Provider(configPath), yaml.Parser()); err != nil {
			return nil, fmt.Errorf("config: load file %q: %w", configPath, err)
		}
	}

	envMap, err := envOverrides(lookupEnv)
	if err != nil {
		return nil, err
	}
	if err := k.Load(confmap.Provider(envMap, delim), nil); err != nil {
		return nil, fmt.Errorf("config: load env: %w", err)
	}

	// flagKey mirrors the env transform for flag names: "-" is the koanf
	// delimiter substitute in flag names, same as "_" is for env vars.
	// "config" (no dash) maps to a harmless unused top-level key.
	flagKey := func(f *flag.Flag) (string, any) {
		key := strings.ReplaceAll(f.Name, "-", delim)
		return key, posflag.FlagVal(fs, f)
	}
	if err := k.Load(posflag.ProviderWithFlag(fs, delim, k, flagKey), nil); err != nil {
		return nil, fmt.Errorf("config: load flags: %w", err)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("config: unmarshal: %w", err)
	}
	cfg.ConfigFile = configPath

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}
