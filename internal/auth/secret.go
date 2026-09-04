package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/utkayd/qurator/internal/config"
)

// SigningKeyFile is the file name, under server.data_dir, where a generated
// credential signing secret is persisted.
const SigningKeyFile = "signing.key"

// signingSecretBytes is the entropy of a generated secret. 32 bytes (256
// bits) matches the HS256 key size the session JWTs use.
const signingSecretBytes = 32

// LoadOrCreateSigningSecret returns the credential signing secret stored at
// path, generating and persisting a fresh one if the file does not exist
// (FR-040). A new secret is 32 bytes from crypto/rand, written base64url
// (unpadded) with mode 0600; the parent directory is created 0700 when
// missing. created reports whether this call generated the secret.
//
// Any failure to read or create the file is returned as an error that names
// QURATOR_AUTH_SIGNING_SECRET as the alternative, so a read-only data dir
// or a wrong path never silently falls back to a weak key. The secret's
// value never appears in an error.
//
// The function does not inspect file permissions on load; callers that
// want to warn about a group- or world-readable key file use
// SigningSecretFilePermissive.
func LoadOrCreateSigningSecret(path string) (secret config.Secret, created bool, err error) {
	if path == "" {
		return "", false, errors.New("auth: signing secret file path is empty" + signingSecretHint)
	}

	raw, readErr := os.ReadFile(path)
	switch {
	case readErr == nil:
		s := strings.TrimSpace(string(raw))
		if s == "" {
			return "", false, fmt.Errorf("auth: signing secret file %s is empty; delete it to generate a new one%s", path, signingSecretHint)
		}
		return config.Secret(s), false, nil
	case errors.Is(readErr, fs.ErrNotExist):
		// fall through to create
	default:
		// Permission denied, EISDIR, and anything else are fatal: we must
		// never guess at a secret because the configured one is unreadable.
		return "", false, fmt.Errorf("auth: read signing secret file %s: %w%s", path, readErr, signingSecretHint)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", false, fmt.Errorf("auth: create data dir for signing secret %s: %w%s", path, err, signingSecretHint)
	}

	buf := make([]byte, signingSecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", false, fmt.Errorf("auth: generate signing secret: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(buf)

	// O_EXCL makes creation atomic with respect to a concurrent first
	// start on the same data dir: exactly one process wins, the other
	// re-reads the winner's file rather than clobbering it.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return LoadOrCreateSigningSecret(path)
		}
		return "", false, fmt.Errorf("auth: create signing secret file %s: %w%s", path, err, signingSecretHint)
	}
	if _, err := f.WriteString(encoded + "\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", false, fmt.Errorf("auth: write signing secret file %s: %w%s", path, err, signingSecretHint)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", false, fmt.Errorf("auth: write signing secret file %s: %w%s", path, err, signingSecretHint)
	}
	return config.Secret(encoded), true, nil
}

// SigningSecretFilePermissive reports whether the signing secret file at
// path is readable by group or others. It returns false when the file
// cannot be stat'ed; the caller has already loaded it by then, so a stat
// failure here is not worth failing on.
func SigningSecretFilePermissive(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return fi.Mode().Perm()&0o077 != 0
}

// signingSecretHint is appended to every fatal error so the operator sees
// the way out that does not involve the filesystem.
const signingSecretHint = " (set QURATOR_AUTH_SIGNING_SECRET to supply a secret directly, or QURATOR_SERVER_DATA_DIR to move the key file)"
