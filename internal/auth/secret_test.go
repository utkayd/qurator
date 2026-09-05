package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateSigningSecret_CreatesWith0600(t *testing.T) {
	dir := t.TempDir()
	// The data dir does not exist yet: it must be created 0700.
	path := filepath.Join(dir, "data", SigningKeyFile)

	sec, created, err := LoadOrCreateSigningSecret(path)
	if err != nil {
		t.Fatalf("LoadOrCreateSigningSecret: %v", err)
	}
	if !created {
		t.Fatal("created = false on first call, want true")
	}
	if !sec.IsSet() {
		t.Fatal("returned secret is empty")
	}
	// 32 random bytes base64url-unpadded is 43 chars.
	if got := len(sec.Reveal()); got != 43 {
		t.Errorf("secret length = %d, want 43", got)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file mode = %o, want 0600", perm)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat data dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("data dir mode = %o, want 0700", perm)
	}
	if SigningSecretFilePermissive(path) {
		t.Error("SigningSecretFilePermissive = true for a 0600 file")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != sec.Reveal() {
		t.Error("file contents do not match the returned secret")
	}
}

func TestLoadOrCreateSigningSecret_IdempotentLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), SigningKeyFile)

	first, created, err := LoadOrCreateSigningSecret(path)
	if err != nil || !created {
		t.Fatalf("first call: created=%v err=%v", created, err)
	}
	second, created, err := LoadOrCreateSigningSecret(path)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if created {
		t.Error("second call reported created = true; the existing key must be reused")
	}
	if first.Reveal() != second.Reveal() {
		t.Error("second call returned a different secret than the first")
	}

	// Two fresh files must not collide: proves the value is random, not fixed.
	other, _, err := LoadOrCreateSigningSecret(filepath.Join(t.TempDir(), SigningKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if other.Reveal() == first.Reveal() {
		t.Error("two independently generated secrets are identical")
	}
}

func TestLoadOrCreateSigningSecret_RefusesDirectoryPath(t *testing.T) {
	dir := t.TempDir() // exists, and is a directory
	_, _, err := LoadOrCreateSigningSecret(dir)
	if err == nil {
		t.Fatal("no error for a directory path")
	}
	if !strings.Contains(err.Error(), "QURATOR_AUTH_SIGNING_SECRET") {
		t.Errorf("error does not name the alternative: %v", err)
	}
}

func TestLoadOrCreateSigningSecret_RefusesEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), SigningKeyFile)
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreateSigningSecret(path); err == nil {
		t.Fatal("no error for an empty key file; must not silently regenerate or use an empty key")
	}
}

func TestLoadOrCreateSigningSecret_RefusesUnwritableLocation(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	parent := t.TempDir()
	ro := filepath.Join(parent, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })

	_, _, err := LoadOrCreateSigningSecret(filepath.Join(ro, "data", SigningKeyFile))
	if err == nil {
		t.Fatal("no error when the data dir cannot be created")
	}
	if !strings.Contains(err.Error(), "QURATOR_AUTH_SIGNING_SECRET") {
		t.Errorf("error does not name the alternative: %v", err)
	}
	_, _, err = LoadOrCreateSigningSecret(filepath.Join(ro, SigningKeyFile))
	if err == nil {
		t.Fatal("no error when the key file cannot be created")
	}
}

func TestSigningSecretFilePermissive(t *testing.T) {
	path := filepath.Join(t.TempDir(), SigningKeyFile)
	if err := os.WriteFile(path, []byte("k"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !SigningSecretFilePermissive(path) {
		t.Error("SigningSecretFilePermissive = false for a 0644 file")
	}
	if SigningSecretFilePermissive(filepath.Join(t.TempDir(), "missing")) {
		t.Error("SigningSecretFilePermissive = true for a missing file")
	}
}
