// Package config loads and validates qurator's runtime configuration.
package config

import "log/slog"

// redacted is the fixed stand-in printed, marshalled, or logged in place of
// any non-empty Secret value. It is intentionally content-free: no length,
// no prefix, no hash — nothing that could narrow the search space of the
// real value.
const redacted = "***"

// Secret wraps a configuration value that must never appear in logs, error
// messages, or serialized output (FR-049). Every formatting and encoding
// hook below yields "***" for a non-empty value and "" for an empty one, so
// callers can still tell "unset" from "set" without ever seeing what was
// set.
//
// The one sanctioned way to read the underlying value is Reveal(). A bare
// `string(secret)` conversion bypasses all of this — the type system cannot
// prevent that cast, which is why a forbidigo lint rule (see .golangci.yml
// and testdata/lint_fixture.go.txt) exists to catch it.
type Secret string

// String implements fmt.Stringer, so %v, %s, fmt.Print, and fmt.Sprint all
// redact the value.
func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return redacted
}

// GoString implements fmt.GoStringer, so %#v redacts the value.
func (s Secret) GoString() string {
	if s == "" {
		return `config.Secret("")`
	}
	return `config.Secret("` + redacted + `")`
}

// MarshalJSON implements json.Marshaler, so encoding/json never emits the
// underlying value.
func (s Secret) MarshalJSON() ([]byte, error) {
	if s == "" {
		return []byte(`""`), nil
	}
	return []byte(`"` + redacted + `"`), nil
}

// MarshalText implements encoding.TextMarshaler, used by encoders (and by
// koanf/mapstructure round-tripping) that prefer it over MarshalJSON.
func (s Secret) MarshalText() ([]byte, error) {
	if s == "" {
		return []byte(""), nil
	}
	return []byte(redacted), nil
}

// LogValue implements slog.LogValuer, so logging a Secret directly (or a
// struct containing one via slog.Any) never writes the underlying value
// into any handler's output.
func (s Secret) LogValue() slog.Value {
	if s == "" {
		return slog.StringValue("")
	}
	return slog.StringValue(redacted)
}

// Reveal returns the underlying secret value. This is the ONLY sanctioned
// way to read it; every other access path on this type is redacted by
// design. Call it only at the point of use (e.g. handing a signing key to
// the JWT library) and never store or log its result.
func (s Secret) Reveal() string {
	return string(s)
}

// IsSet reports whether the secret has a non-empty value, without
// revealing it.
func (s Secret) IsSet() bool {
	return s != ""
}
