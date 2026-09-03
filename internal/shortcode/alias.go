package shortcode

import (
	"errors"
	"fmt"
	"strings"
)

const (
	// AliasMinLen and AliasMaxLen bound alias length after normalization.
	AliasMinLen = 3
	AliasMaxLen = 64
)

// ErrAliasInvalid is the sentinel every alias validation error wraps, so
// callers can test errors.Is(err, ErrAliasInvalid) without enumerating every
// specific cause.
var ErrAliasInvalid = errors.New("shortcode: invalid alias")

// Specific alias validation failures. Each wraps ErrAliasInvalid.
var (
	ErrAliasTooShort     = fmt.Errorf("%w: shorter than %d characters", ErrAliasInvalid, AliasMinLen)
	ErrAliasTooLong      = fmt.Errorf("%w: longer than %d characters", ErrAliasInvalid, AliasMaxLen)
	ErrAliasBadChar      = fmt.Errorf("%w: contains a character outside [a-z0-9-]", ErrAliasInvalid)
	ErrAliasBadEdge      = fmt.Errorf("%w: must start and end with a letter or digit", ErrAliasInvalid)
	ErrAliasDoubleHyphen = fmt.Errorf("%w: contains consecutive hyphens", ErrAliasInvalid)
	ErrAliasReserved     = fmt.Errorf("%w: reserved word", ErrAliasInvalid)
	// ErrAliasLooksGenerated is returned for an alias that matches the exact
	// shape produced by Generate, which would make generated codes and
	// aliases indistinguishable.
	ErrAliasLooksGenerated = fmt.Errorf("%w: matches the generated-code shape", ErrAliasInvalid)
)

// NormalizeAlias lowercases and trims s. Validation and storage both operate
// on this normalized form so that "Spring-Sale" and "spring-sale" are the
// same alias.
func NormalizeAlias(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ValidateAlias normalizes s and checks it against the alias rules from
// research.md §7: 3-64 characters, charset [a-z0-9-], alphanumeric first and
// last character, no consecutive hyphens, not a reserved word, and not
// matching the generated-code shape. It returns one of the Err* sentinels
// above (all wrapping ErrAliasInvalid) on failure, or nil if s normalizes to
// a valid alias.
func ValidateAlias(s string) error {
	a := NormalizeAlias(s)

	if len(a) < AliasMinLen {
		return ErrAliasTooShort
	}
	if len(a) > AliasMaxLen {
		return ErrAliasTooLong
	}

	for i := 0; i < len(a); i++ {
		c := a[i]
		if !isAliasChar(c) {
			return ErrAliasBadChar
		}
	}

	if !isAlphanumeric(a[0]) || !isAlphanumeric(a[len(a)-1]) {
		return ErrAliasBadEdge
	}

	if strings.Contains(a, "--") {
		return ErrAliasDoubleHyphen
	}

	if IsReserved(a) {
		return ErrAliasReserved
	}

	if IsGeneratedShape(a) {
		return ErrAliasLooksGenerated
	}

	return nil
}

func isAlphanumeric(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

func isAliasChar(c byte) bool {
	return isAlphanumeric(c) || c == '-'
}
