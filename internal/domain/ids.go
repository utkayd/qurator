package domain

import (
	"crypto/rand"
	"fmt"
)

// idAlphabet is lowercase Crockford base32: digits plus letters excluding i, l, o, u.
// Single-case so the stored value is its own canonical form; no visually confusable
// characters so IDs survive being read off a screen.
const idAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

const idRandomLen = 16 // 16 × 5 bits = 80 bits

// NewID returns prefix + "_" + 16 random Crockford-base32 characters.
func NewID(prefix string) string {
	return prefix + "_" + RandomCrockford(idRandomLen)
}

// RandomCrockford returns n characters drawn uniformly from idAlphabet using crypto/rand.
// Each byte is masked to 5 bits, so there is no modulo bias.
func RandomCrockford(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("crypto/rand unavailable: %v", err)) // unrecoverable; refuse to mint weak IDs
	}
	for i := range buf {
		buf[i] = idAlphabet[buf[i]&0x1f]
	}
	return string(buf)
}

// Convenience constructors with the prefixes fixed in data-model.md.
func NewUserID() string  { return NewID("usr") }
func NewTokenID() string { return NewID("tok") }
func NewCodeID() string  { return NewID("cod") }
