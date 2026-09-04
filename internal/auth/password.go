package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters: RFC 9106 §4 "lighter" profile (research.md §2).
const (
	argonTime    uint32 = 3
	argonMemory  uint32 = 64 * 1024 // KiB
	argonThreads uint8  = 4
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

var errMalformedPHC = errors.New("auth: malformed argon2id PHC string")

// HashPassword returns a PHC-encoded Argon2id hash:
// $argon2id$v=19$m=65536,t=3,p=4$<b64 salt>$<b64 hash> (base64 without padding).
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks password against a PHC string, reading the parameters from the
// string itself so hashes survive a future parameter change. The KDF always runs to
// completion before the constant-time compare, so a wrong password costs the same as a
// right one.
func VerifyPassword(phc, password string) (bool, error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errMalformedPHC
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errMalformedPHC
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || t == 0 || p == 0 {
		return false, errMalformedPHC
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false, errMalformedPHC
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errMalformedPHC
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

var (
	dummyOnce sync.Once
	dummyPHC  string
)

// DummyVerify burns one Argon2id verification against a throwaway hash. Sign-in calls it
// when the email is unknown (or the account has no password) so the response time does
// not reveal whether the account exists.
func DummyVerify(password string) {
	dummyOnce.Do(func() {
		h, err := HashPassword("qurator-dummy-password")
		if err != nil {
			h = ""
		}
		dummyPHC = h
	})
	if dummyPHC != "" {
		_, _ = VerifyPassword(dummyPHC, password)
	}
}
