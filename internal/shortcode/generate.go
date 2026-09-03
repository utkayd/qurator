// Package shortcode implements generation and validation of the short codes and
// aliases that identify dynamic QR codes, per research.md §7.
package shortcode

import (
	"regexp"

	"github.com/utkayd/qurator/internal/domain"
)

// GeneratedLen is the length in characters of a machine-generated short code.
// 12 characters of lowercase Crockford base32 (5 bits/char) = 60 bits of entropy,
// sized for enumeration resistance rather than collision avoidance — see
// research.md §7.
const GeneratedLen = 12

// GeneratedShape matches the exact character shape produced by Generate. Aliases
// are rejected if they match this shape so the two code kinds stay
// distinguishable and the generator's space cannot be poisoned by a custom alias.
var GeneratedShape = regexp.MustCompile(`^[0-9a-hjkmnp-tv-z]{12}$`)

// Generate returns a new 12-character short code drawn uniformly from the
// lowercase Crockford base32 alphabet (excludes i, l, o, u) using crypto/rand.
func Generate() string {
	return domain.RandomCrockford(GeneratedLen)
}

// IsGeneratedShape reports whether s has the exact shape produced by Generate,
// regardless of whether it was actually generated or merely looks like it was.
func IsGeneratedShape(s string) bool {
	return GeneratedShape.MatchString(s)
}
