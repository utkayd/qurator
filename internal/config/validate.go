package config

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

var (
	validDBDrivers   = map[string]bool{"sqlite": true, "postgres": true}
	validBlobDrivers = map[string]bool{"fs": true, "s3": true}
)

// Validate checks c for every constraint required before qurator may
// start, returning every violation found joined via errors.Join rather
// than stopping at the first one, so an operator sees the whole list of
// problems in a single run.
func (c *Config) Validate() error {
	var errs []error

	// FR-040: an empty auth.signing_secret is NOT a validation error. When
	// dev mode is off the binary generates one and persists it under
	// server.data_dir (see auth.LoadOrCreateSigningSecret); it refuses to
	// start only if that file can neither be read nor created. The data
	// dir itself must be named so that path is well-defined.
	if !c.Auth.DevMode && !c.Auth.SigningSecret.IsSet() && c.Server.DataDir == "" {
		errs = append(errs, errors.New(
			"config: server.data_dir is empty and auth.signing_secret is not set: "+
				"set QURATOR_SERVER_DATA_DIR (where signing.key is persisted) or QURATOR_AUTH_SIGNING_SECRET"))
	}

	// Fail closed: forward-auth with no trusted CIDRs would trust the
	// header from any peer.
	if c.ForwardAuth.Enabled && len(c.ForwardAuth.TrustedCIDRs) == 0 {
		errs = append(errs, errors.New(
			"config: forward_auth.enabled is true but forward_auth.trusted_cidrs is empty "+
				"(refusing to trust the identity header from any peer)"))
	}
	for _, cidr := range c.ForwardAuth.TrustedCIDRs {
		if _, ipnet, err := net.ParseCIDR(cidr); err == nil {
			if ones, _ := ipnet.Mask.Size(); ones == 0 {
				errs = append(errs, fmt.Errorf("config: forward_auth.trusted_cidrs contains %q, which trusts every peer on the internet; list only your proxy's addresses", cidr))
			}
		} else {
			errs = append(errs, fmt.Errorf("config: forward_auth.trusted_cidrs: invalid CIDR %q: %w", cidr, err))
		}
	}

	if !validDBDrivers[c.DB.Driver] {
		errs = append(errs, fmt.Errorf("config: db.driver must be one of sqlite, postgres, got %q", c.DB.Driver))
	}

	if !validBlobDrivers[c.Blob.Driver] {
		errs = append(errs, fmt.Errorf("config: blob.driver must be one of fs, s3, got %q", c.Blob.Driver))
	}
	if c.Blob.Driver == "s3" {
		if c.Blob.S3.Endpoint == "" {
			errs = append(errs, errors.New("config: blob.s3.endpoint is required when blob.driver is s3"))
		}
		if c.Blob.S3.Bucket == "" {
			errs = append(errs, errors.New("config: blob.s3.bucket is required when blob.driver is s3"))
		}
	}

	if len(c.Codes.AllowedSchemes) == 0 {
		errs = append(errs, errors.New("config: codes.allowed_schemes must not be empty"))
	}
	for _, scheme := range c.Codes.AllowedSchemes {
		if scheme != strings.ToLower(scheme) {
			errs = append(errs, fmt.Errorf("config: codes.allowed_schemes entries must be lowercase, got %q", scheme))
		}
	}

	if c.Render.MaxPx <= 0 {
		errs = append(errs, fmt.Errorf("config: render.max_px must be > 0, got %d", c.Render.MaxPx))
	}
	if c.Render.MaxDuration <= 0 {
		errs = append(errs, fmt.Errorf("config: render.max_duration must be > 0, got %s", c.Render.MaxDuration))
	}
	if c.Render.MaxPayloadBytes <= 0 {
		errs = append(errs, fmt.Errorf("config: render.max_payload_bytes must be > 0, got %d", c.Render.MaxPayloadBytes))
	}

	if c.Analytics.RetentionDays < 1 {
		errs = append(errs, fmt.Errorf("config: analytics.retention_days must be >= 1, got %d", c.Analytics.RetentionDays))
	}

	// Bootstrap email and password must be configured together, or not
	// at all.
	if (c.Auth.BootstrapEmail != "") != c.Auth.BootstrapPassword.IsSet() {
		errs = append(errs, errors.New(
			"config: auth.bootstrap_email and auth.bootstrap_password must both be set, or both left empty"))
	}

	if _, err := parseLogLevel(c.Log.Level); err != nil {
		errs = append(errs, fmt.Errorf("config: log.level: %w", err))
	}

	return errors.Join(errs...)
}

// parseLogLevel validates that level is one of slog's recognised names.
// It intentionally does not import log/slog's private level parsing; the
// accepted set mirrors slog.Level's four named levels, case-insensitively,
// which is all qurator ever configures.
func parseLogLevel(level string) (string, error) {
	switch strings.ToLower(level) {
	case "debug", "info", "warn", "error":
		return strings.ToLower(level), nil
	default:
		return "", fmt.Errorf("must be one of debug, info, warn, error, got %q", level)
	}
}
