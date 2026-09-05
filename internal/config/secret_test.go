package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

const testSecretValue = "hunter2-xyz"

// assertNoLeak fails the test if the literal secret value appears anywhere
// in got, and reports the string that leaked it for debugging.
func assertNoLeak(t *testing.T, label, got string) {
	t.Helper()
	if strings.Contains(got, testSecretValue) {
		t.Errorf("%s leaked the secret value: %q", label, got)
	}
}

func TestSecret_FmtVerbs(t *testing.T) {
	s := Secret(testSecretValue)

	assertNoLeak(t, "%v", fmt.Sprintf("%v", s))
	assertNoLeak(t, "%+v", fmt.Sprintf("%+v", s))
	assertNoLeak(t, "%#v", fmt.Sprintf("%#v", s))
	assertNoLeak(t, "%s", s.String())
	assertNoLeak(t, "%q", fmt.Sprintf("%q", s))
	assertNoLeak(t, "fmt.Sprint", fmt.Sprint(s))
}

func TestSecret_JSONMarshal(t *testing.T) {
	type holder struct {
		Token Secret `json:"token"`
	}
	h := holder{Token: Secret(testSecretValue)}

	b, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	assertNoLeak(t, "json.Marshal", string(b))
}

func TestSecret_SlogAttribute(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	logger.Info("test event", slog.Any("secret", Secret(testSecretValue)))
	assertNoLeak(t, "slog.Any attribute", buf.String())
}

func TestSecret_SlogStructField(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	type holder struct {
		Token Secret
	}
	h := holder{Token: Secret(testSecretValue)}

	// A struct value logged via slog.Any is rendered with %v/%+v under
	// the hood by the default JSON handler when the value carries no
	// slog.LogValuer of its own; the *field* type (Secret) is the one
	// that must resist that, which it does via LogValue.
	logger.Info("test event", slog.Any("holder", h))
	assertNoLeak(t, "slog.Any(struct-with-secret)", buf.String())
}

func TestSecret_FullConfigFormatting(t *testing.T) {
	cfg := Config{}
	cfg.Auth.SigningSecret = Secret(testSecretValue)
	cfg.Auth.BootstrapPassword = Secret(testSecretValue)
	cfg.Blob.S3.AccessKey = Secret(testSecretValue)
	cfg.Blob.S3.SecretKey = Secret(testSecretValue)

	assertNoLeak(t, "%+v of Config", fmt.Sprintf("%+v", cfg))

	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal(Config): %v", err)
	}
	assertNoLeak(t, "json.Marshal(Config)", string(b))
}

func TestSecret_Reveal(t *testing.T) {
	s := Secret(testSecretValue)
	if got := s.Reveal(); got != testSecretValue {
		t.Errorf("Reveal() = %q, want %q", got, testSecretValue)
	}
}

func TestSecret_IsSet(t *testing.T) {
	if Secret("").IsSet() {
		t.Error("empty Secret.IsSet() = true, want false")
	}
	if !Secret("x").IsSet() {
		t.Error("non-empty Secret.IsSet() = false, want true")
	}
}

func TestSecret_EmptyIsDistinguishable(t *testing.T) {
	empty := Secret("")
	set := Secret(testSecretValue)

	if empty.String() == set.String() {
		t.Error("empty and non-empty secrets render identically; emptiness must stay detectable")
	}
	if empty.String() != "" {
		t.Errorf("empty Secret.String() = %q, want empty string", empty.String())
	}
	if set.String() != redacted {
		t.Errorf("non-empty Secret.String() = %q, want %q", set.String(), redacted)
	}
}
