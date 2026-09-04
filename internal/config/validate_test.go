package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validConfig returns a Config that satisfies every Validate() rule, so
// each table test case can flip exactly one field into invalid territory.
func validConfig() Config {
	var c Config
	c.Server.Listen = ":8080"
	c.Server.DataDir = "./data"
	c.DB.Driver = "sqlite"
	c.DB.DSN = "./data/qurator.db"
	c.Blob.Driver = "fs"
	c.Blob.Path = "./data/blobs"
	c.Auth.SigningSecret = Secret("a-real-signing-secret")
	c.Auth.DevMode = false
	c.Codes.AllowedSchemes = []string{"http", "https"}
	c.Render.MaxPx = 4096
	c.Render.MaxDuration = 2_000_000_000 // 2s in ns
	c.Render.MaxPayloadBytes = 2953
	c.Analytics.RetentionDays = 365
	c.Log.Level = "info"
	return c
}

func TestValidate_Rules(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string // substring expected in the joined error
	}{
		{
			// FR-040: an empty secret is generated and persisted at startup,
			// so validation no longer refuses it.
			name: "signing secret empty and dev mode off is fine",
			mutate: func(c *Config) {
				c.Auth.SigningSecret = ""
				c.Auth.DevMode = false
			},
			wantErr: "",
		},
		{
			name: "signing secret empty with nowhere to persist one",
			mutate: func(c *Config) {
				c.Auth.SigningSecret = ""
				c.Auth.DevMode = false
				c.Server.DataDir = ""
			},
			wantErr: "QURATOR_SERVER_DATA_DIR",
		},
		{
			name: "signing secret empty, no data dir, but dev mode on is fine",
			mutate: func(c *Config) {
				c.Auth.SigningSecret = ""
				c.Auth.DevMode = true
				c.Server.DataDir = ""
			},
			wantErr: "",
		},
		{
			name: "signing secret empty but dev mode on is fine",
			mutate: func(c *Config) {
				c.Auth.SigningSecret = ""
				c.Auth.DevMode = true
			},
			wantErr: "",
		},
		{
			name: "forward-auth enabled with no trusted CIDRs",
			mutate: func(c *Config) {
				c.ForwardAuth.Enabled = true
				c.ForwardAuth.TrustedCIDRs = nil
			},
			wantErr: "trusted_cidrs is empty",
		},
		{
			name: "forward-auth enabled with an invalid CIDR",
			mutate: func(c *Config) {
				c.ForwardAuth.Enabled = true
				c.ForwardAuth.TrustedCIDRs = []string{"not-a-cidr"}
			},
			wantErr: "invalid CIDR",
		},
		{
			name: "forward-auth enabled with a valid CIDR is fine",
			mutate: func(c *Config) {
				c.ForwardAuth.Enabled = true
				c.ForwardAuth.TrustedCIDRs = []string{"10.0.0.0/8"}
			},
			wantErr: "",
		},
		{
			name:    "unknown db driver",
			mutate:  func(c *Config) { c.DB.Driver = "mysql" },
			wantErr: "db.driver",
		},
		{
			name:    "unknown blob driver",
			mutate:  func(c *Config) { c.Blob.Driver = "azure" },
			wantErr: "blob.driver",
		},
		{
			name: "s3 driver missing endpoint and bucket",
			mutate: func(c *Config) {
				c.Blob.Driver = "s3"
				c.Blob.S3.Endpoint = ""
				c.Blob.S3.Bucket = ""
			},
			wantErr: "blob.s3.endpoint is required",
		},
		{
			name: "s3 driver with endpoint and bucket is fine",
			mutate: func(c *Config) {
				c.Blob.Driver = "s3"
				c.Blob.S3.Endpoint = "s3.example.com"
				c.Blob.S3.Bucket = "qurator"
			},
			wantErr: "",
		},
		{
			name:    "empty allowed schemes",
			mutate:  func(c *Config) { c.Codes.AllowedSchemes = nil },
			wantErr: "allowed_schemes must not be empty",
		},
		{
			name:    "non-lowercase scheme",
			mutate:  func(c *Config) { c.Codes.AllowedSchemes = []string{"HTTPS"} },
			wantErr: "must be lowercase",
		},
		{
			name:    "max_px zero",
			mutate:  func(c *Config) { c.Render.MaxPx = 0 },
			wantErr: "render.max_px",
		},
		{
			name:    "max_duration zero",
			mutate:  func(c *Config) { c.Render.MaxDuration = 0 },
			wantErr: "render.max_duration",
		},
		{
			name:    "max_payload_bytes negative",
			mutate:  func(c *Config) { c.Render.MaxPayloadBytes = -1 },
			wantErr: "render.max_payload_bytes",
		},
		{
			name:    "retention days zero",
			mutate:  func(c *Config) { c.Analytics.RetentionDays = 0 },
			wantErr: "retention_days",
		},
		{
			name:    "bootstrap email without password",
			mutate:  func(c *Config) { c.Auth.BootstrapEmail = "admin@example.com" },
			wantErr: "bootstrap_email and auth.bootstrap_password",
		},
		{
			name: "bootstrap password without email",
			mutate: func(c *Config) {
				c.Auth.BootstrapPassword = Secret("s3cret")
			},
			wantErr: "bootstrap_email and auth.bootstrap_password",
		},
		{
			name: "bootstrap email and password together is fine",
			mutate: func(c *Config) {
				c.Auth.BootstrapEmail = "admin@example.com"
				c.Auth.BootstrapPassword = Secret("s3cret")
			},
			wantErr: "",
		},
		{
			name:    "unparseable log level",
			mutate:  func(c *Config) { c.Log.Level = "verbose" },
			wantErr: "log.level",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			tc.mutate(&c)
			err := c.Validate()

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidate_JoinsAllProblems(t *testing.T) {
	var c Config // zero value violates several rules at once
	c.Auth.DevMode = false

	err := c.Validate()
	if err == nil {
		t.Fatal("Validate() = nil for a zero-value Config, want multiple errors")
	}
	msg := err.Error()
	for _, want := range []string{"data_dir", "db.driver", "blob.driver", "allowed_schemes"} {
		if !strings.Contains(msg, want) {
			t.Errorf("joined error missing %q; got: %s", want, msg)
		}
	}
}

// noEnv is a lookupEnv that reports every key as unset, for hermetic
// tests that must not depend on (or touch) the real process environment.
func noEnv(string) (string, bool) { return "", false }

// mapEnv builds a lookupEnv backed by a fixed map, for hermetic tests.
func mapEnv(m map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

func TestLoad_EmptyEnvSucceeds(t *testing.T) {
	// Constitution Principle I / FR-040: an empty environment is a valid
	// configuration. The signing secret is left empty for the binary to
	// generate into <server.data_dir>/signing.key; Load itself never
	// touches the filesystem for it.
	cfg, err := Load(nil, noEnv)
	if err != nil {
		t.Fatalf("Load(empty env, no args) = %v, want success", err)
	}
	if cfg.Auth.SigningSecret.IsSet() {
		t.Error("Auth.SigningSecret is set from an empty env, want empty")
	}
	if cfg.Auth.DevMode {
		t.Error("Auth.DevMode = true from an empty env, want false")
	}
	if cfg.Server.DataDir != "./data" {
		t.Errorf("Server.DataDir = %q, want ./data", cfg.Server.DataDir)
	}
}

func TestLoad_DataDirFromEnv(t *testing.T) {
	cfg, err := Load(nil, mapEnv(map[string]string{"QURATOR_SERVER_DATA_DIR": "/var/lib/qurator"}))
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if cfg.Server.DataDir != "/var/lib/qurator" {
		t.Errorf("Server.DataDir = %q, want /var/lib/qurator", cfg.Server.DataDir)
	}
}

func TestLoad_DevModeOnlySucceedsWithSafeDefaults(t *testing.T) {
	env := mapEnv(map[string]string{"QURATOR_AUTH_DEV_MODE": "true"})

	cfg, err := Load(nil, env)
	if err != nil {
		t.Fatalf("Load() = %v, want success", err)
	}

	// FR-048: every exposure-widening field must default off/empty.
	if cfg.Ephemeral.Public {
		t.Error("Ephemeral.Public defaults to true, want false")
	}
	if cfg.ForwardAuth.Enabled {
		t.Error("ForwardAuth.Enabled defaults to true, want false")
	}
	if !cfg.Auth.DevMode {
		t.Error("Auth.DevMode = false, want true (explicitly set)")
	}
	if cfg.Server.MetricsListen != "" {
		t.Errorf("Server.MetricsListen = %q, want empty (disabled by default)", cfg.Server.MetricsListen)
	}

	// Spot-check the rest of the documented defaults.
	if cfg.Server.Listen != ":8080" {
		t.Errorf("Server.Listen = %q, want :8080", cfg.Server.Listen)
	}
	if cfg.DB.Driver != "sqlite" || cfg.DB.DSN != "./data/qurator.db" {
		t.Errorf("DB = %+v, want sqlite ./data/qurator.db", cfg.DB)
	}
	if cfg.Blob.Driver != "fs" || cfg.Blob.Path != "./data/blobs" {
		t.Errorf("Blob = %+v, want fs ./data/blobs", cfg.Blob)
	}
	if cfg.Ephemeral.RateLimitPerMinute != 60 {
		t.Errorf("Ephemeral.RateLimitPerMinute = %d, want 60", cfg.Ephemeral.RateLimitPerMinute)
	}
	if got, want := cfg.Codes.AllowedSchemes, []string{"http", "https"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Codes.AllowedSchemes = %v, want %v", got, want)
	}
	if cfg.Render.MaxPx != 4096 || cfg.Render.MaxPayloadBytes != 2953 {
		t.Errorf("Render bounds = %+v, want max_px=4096 max_payload_bytes=2953", cfg.Render)
	}
	if cfg.Render.MaxDuration.String() != "2s" {
		t.Errorf("Render.MaxDuration = %s, want 2s", cfg.Render.MaxDuration)
	}
	if cfg.Analytics.RetentionDays != 365 {
		t.Errorf("Analytics.RetentionDays = %d, want 365", cfg.Analytics.RetentionDays)
	}
	if cfg.Analytics.FlushInterval.String() != "500ms" {
		t.Errorf("Analytics.FlushInterval = %s, want 500ms", cfg.Analytics.FlushInterval)
	}
	if cfg.Log.Level != "info" || cfg.Log.Format != "json" {
		t.Errorf("Log = %+v, want info/json", cfg.Log)
	}
}

func TestLoad_Precedence_DefaultFileEnvFlag(t *testing.T) {
	// default: server.listen == ":8080" (checked separately above).

	// file layer overrides default.
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "qurator.yaml")
	if err := os.WriteFile(cfgFile, []byte("server:\n  listen: \":9000\"\n"), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	env := mapEnv(map[string]string{
		"QURATOR_AUTH_DEV_MODE": "true",
		"QURATOR_CONFIG":        cfgFile,
	})
	cfg, err := Load(nil, env)
	if err != nil {
		t.Fatalf("Load(file only) = %v", err)
	}
	if cfg.Server.Listen != ":9000" {
		t.Fatalf("Server.Listen = %q after file layer, want :9000", cfg.Server.Listen)
	}

	// env layer overrides file.
	env = mapEnv(map[string]string{
		"QURATOR_AUTH_DEV_MODE": "true",
		"QURATOR_CONFIG":        cfgFile,
		"QURATOR_SERVER_LISTEN": ":9100",
	})
	cfg, err = Load(nil, env)
	if err != nil {
		t.Fatalf("Load(file+env) = %v", err)
	}
	if cfg.Server.Listen != ":9100" {
		t.Fatalf("Server.Listen = %q after env layer, want :9100", cfg.Server.Listen)
	}

	// flag layer overrides env.
	cfg, err = Load([]string{"--server-listen=:9200"}, env)
	if err != nil {
		t.Fatalf("Load(file+env+flag) = %v", err)
	}
	if cfg.Server.Listen != ":9200" {
		t.Fatalf("Server.Listen = %q after flag layer, want :9200", cfg.Server.Listen)
	}
}

func TestLoad_NilLookupEnvFallsBackToOSEnv(t *testing.T) {
	// A nil lookupEnv must fall back to the real process environment
	// rather than panicking; use t.Setenv so it is restored afterward.
	t.Setenv("QURATOR_AUTH_DEV_MODE", "true")

	cfg, err := Load(nil, nil)
	if err != nil {
		t.Fatalf("Load(nil lookupEnv) = %v", err)
	}
	if !cfg.Auth.DevMode {
		t.Error("Auth.DevMode = false, want true from real process env")
	}
}

func TestConfig_JSONRoundTripHidesSecrets(t *testing.T) {
	cfg, err := Load(nil, mapEnv(map[string]string{
		"QURATOR_AUTH_DEV_MODE":       "true",
		"QURATOR_AUTH_SIGNING_SECRET": testSecretValue,
	}))
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(b), testSecretValue) {
		t.Fatalf("marshaled Config leaked the signing secret: %s", b)
	}
}
