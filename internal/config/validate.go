package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/utkayd/qurator/internal/domain"
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
	if origin, err := domain.ParseScanOrigin(c.Server.BaseURL); err != nil {
		errs = append(errs, fmt.Errorf("config: server.base_url: %w", err))
	} else if origin != nil {
		c.Server.BaseURL = origin.String()
	}

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

	// Spec 003 batch bounds. Zero would make every batch oversized; the upper caps keep
	// one request from monopolising the instance.
	if c.Codes.BatchMax < 1 || c.Codes.BatchMax > 5000 {
		errs = append(errs, fmt.Errorf("config: codes.batch_max must be between 1 and 5000, got %d", c.Codes.BatchMax))
	}
	if c.Codes.BatchWorkers < 1 || c.Codes.BatchWorkers > 64 {
		errs = append(errs, fmt.Errorf("config: codes.batch_workers must be between 1 and 64, got %d", c.Codes.BatchWorkers))
	}

	errs = append(errs, c.validateImages()...)

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

var validURLModes = map[string]bool{"instance": true, "public": true, "presigned": true}

// validateImages checks the spec 003 image addressing rules (FR-201, US1 scenario 4,
// US2 scenario 2) and normalises images.public_base_url in place (no trailing slash).
func (c *Config) validateImages() []error {
	var errs []error
	mode := c.Images.URLMode
	if !validURLModes[mode] {
		errs = append(errs, fmt.Errorf("config: images.url_mode must be one of instance, public, presigned, got %q", mode))
		return errs
	}
	if mode != "instance" && c.Blob.Driver != "s3" {
		errs = append(errs, fmt.Errorf("config: images.url_mode %q needs an object store that can address its objects, but blob.driver is %q (only s3 can)", mode, c.Blob.Driver))
	}
	if mode == "public" && c.Images.PublicBaseURL == "" {
		errs = append(errs, errors.New("config: images.public_base_url is required when images.url_mode is public"))
	}
	if raw := c.Images.PublicBaseURL; raw != "" {
		u, err := url.Parse(raw)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("config: images.public_base_url: %w", err))
		case (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
			errs = append(errs, fmt.Errorf("config: images.public_base_url must be an absolute http or https URL, got %q", raw))
		default:
			c.Images.PublicBaseURL = strings.TrimRight(raw, "/")
		}
	}
	if c.Images.PresignTTL <= 0 {
		errs = append(errs, fmt.Errorf("config: images.presign_ttl must be > 0, got %s", c.Images.PresignTTL))
	}
	if !c.Images.ServeViaInstance && mode == "instance" {
		errs = append(errs, errors.New("config: images.serve_via_instance is false but images.url_mode is instance: no image could be reached; set url_mode to public or presigned"))
	}
	return errs
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
