package auth

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

// idEncoding renders 10 random bytes as 16 lowercase characters from [a-z2-7], which
// satisfies the OpenAPI `^(usr|tok)_[0-9A-Za-z]{16}$` identifier shape.
var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// idBytes is the number of random bytes behind an identifier (80 bits).
const idBytes = 10

// encodeID renders identifier material with the given prefix.
func encodeID(prefix string, material []byte) string {
	return prefix + strings.ToLower(idEncoding.EncodeToString(material[:idBytes]))
}

// newID returns a fresh identifier such as "usr_" + 16 chars.
func newID(prefix string) string {
	b := make([]byte, idBytes)
	if _, err := rand.Read(b); err != nil {
		panic("auth: crypto/rand unavailable: " + err.Error())
	}
	return encodeID(prefix, b)
}
