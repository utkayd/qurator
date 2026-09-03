package shortcode

import (
	"errors"
	"strings"
	"testing"
)

func TestGenerateShapeAndAlphabet(t *testing.T) {
	code := Generate()

	if len(code) != GeneratedLen {
		t.Fatalf("Generate() length = %d, want %d", len(code), GeneratedLen)
	}
	if !IsGeneratedShape(code) {
		t.Fatalf("Generate() = %q does not match GeneratedShape", code)
	}
	for _, excluded := range []byte{'i', 'l', 'o', 'u'} {
		if strings.IndexByte(code, excluded) != -1 {
			t.Fatalf("Generate() = %q contains excluded character %q", code, excluded)
		}
	}
	const allowed = "0123456789abcdefghjkmnpqrstvwxyz"
	for _, c := range code {
		if strings.IndexRune(allowed, c) == -1 {
			t.Fatalf("Generate() = %q contains character %q outside the Crockford alphabet", code, c)
		}
	}
}

func TestGenerateUniqueness(t *testing.T) {
	const n = 10000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		code := Generate()
		if _, dup := seen[code]; dup {
			t.Fatalf("Generate() produced duplicate code %q after %d calls", code, i)
		}
		seen[code] = struct{}{}
	}
}

func TestNormalizeAlias(t *testing.T) {
	got := NormalizeAlias("  Spring-Sale  ")
	if got != "spring-sale" {
		t.Fatalf("NormalizeAlias(%q) = %q, want %q", "  Spring-Sale  ", got, "spring-sale")
	}
}

func TestValidateAliasValid(t *testing.T) {
	cases := []string{
		"spring-sale",
		"abc",
		"a1b2",
		strings.Repeat("a", 64), // exactly max length
		"Spring-Sale",           // normalizes to spring-sale
		"verify-account",        // NOT on the reserved list, only the bare word "verify" is
	}
	for _, alias := range cases {
		if err := ValidateAlias(alias); err != nil {
			t.Errorf("ValidateAlias(%q) = %v, want nil", alias, err)
		}
	}
}

func TestValidateAliasInvalid(t *testing.T) {
	cases := []struct {
		name  string
		alias string
		want  error
	}{
		{"too short", "ab", ErrAliasTooShort},
		{"too long", strings.Repeat("a", 65), ErrAliasTooLong},
		{"bad char underscore", "Spring_Sale", ErrAliasBadChar},
		{"bad edge leading hyphen", "-abc", ErrAliasBadEdge},
		{"bad edge trailing hyphen", "abc-", ErrAliasBadEdge},
		{"double hyphen middle", "a--b", ErrAliasDoubleHyphen},
		{"double hyphen punycode prefix", "xn--foo", ErrAliasDoubleHyphen},
		{"reserved lowercase", "healthz", ErrAliasReserved},
		{"reserved uppercase normalizes first", "HEALTHZ", ErrAliasReserved},
		{"reserved abuse word", "verify", ErrAliasReserved},
		{"looks generated", "0123456789ab", ErrAliasLooksGenerated},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAlias(tc.alias)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ValidateAlias(%q) = %v, want %v", tc.alias, err, tc.want)
			}
			if !errors.Is(err, ErrAliasInvalid) {
				t.Fatalf("ValidateAlias(%q) = %v, want it to satisfy errors.Is(err, ErrAliasInvalid)", tc.alias, err)
			}
		})
	}
}

func TestIsReserved(t *testing.T) {
	cases := []struct {
		word string
		want bool
	}{
		{"healthz", true},
		{"HEALTHZ", true},
		{"verify", true},
		{"robots.txt", true},
		{"verify-account", false},
		{"spring-sale", false},
	}
	for _, tc := range cases {
		if got := IsReserved(tc.word); got != tc.want {
			t.Errorf("IsReserved(%q) = %v, want %v", tc.word, got, tc.want)
		}
	}
}

func TestReservedWordsSortedAndNonEmpty(t *testing.T) {
	words := ReservedWords()
	if len(words) == 0 {
		t.Fatal("ReservedWords() returned no words")
	}
	for i := 1; i < len(words); i++ {
		if words[i-1] >= words[i] {
			t.Fatalf("ReservedWords() not sorted: %q >= %q at index %d", words[i-1], words[i], i)
		}
	}
}
