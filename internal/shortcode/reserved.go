package shortcode

import "sort"

// reserved is the reserved-word list from research.md §7: aliases matching any
// of these words (case-insensitively) are refused with alias_reserved rather
// than alias_invalid. It covers six groups:
//
//  1. Instance routes — every static path segment registered by the router
//     (openapi.yaml and the /ui/ console tree), so a reserved alias can never
//     shadow a real endpoint.
//  2. Operational endpoints — health/readiness/metrics surfaces.
//  3. Auth paths — sign-in/out and account-lifecycle routes.
//  4. Resource names — nouns the API and console use for their own entities.
//  5. Well-known files — paths crawlers and browsers request unprompted.
//  6. Abuse-adjacent words — terms a phishing lure would want, even though no
//     route uses them (e.g. a printed "/r/verify-account" would be credible).
//
// tests/arch/routes_test.go cross-checks group 1 against the live router so
// this list cannot rot silently as routes are added.
var reserved = buildReserved()

func buildReserved() map[string]struct{} {
	words := []string{
		// 1. Instance routes (static path segments from openapi.yaml + the /ui/ console tree)
		"r", "i", "ui", "assets",
		"v1", "qr", "codes", "disable", "enable", "analytics",
		"auth", "signin", "signout", "me",
		"tokens", "admin", "aliases", "export",

		// 2. Operational endpoints
		"healthz", "readyz", "metrics", "health", "ready", "status", "ping", "version",

		// 3. Auth paths
		"login", "logout", "register", "signup", "password",
		"reset-password", "forgot-password", "change-password",

		// 4. Resource names
		"code", "user", "users", "token", "alias", "api", "console",
		"dashboard", "settings", "static",

		// 5. Well-known files
		"robots.txt", "favicon.ico", "sitemap.xml", "humans.txt", "security.txt",
		".well-known",

		// 6. Abuse-adjacent words
		"verify", "secure", "security", "billing", "account", "support", "help",
		"bank", "paypal", "confirm", "update", "unsubscribe",
	}

	m := make(map[string]struct{}, len(words))
	for _, w := range words {
		m[w] = struct{}{}
	}
	return m
}

// IsReserved reports whether s (compared case-insensitively) is on the
// reserved-word list. Callers should normalize s first if they want a single
// definition of "case-insensitive"; IsReserved lowercases ASCII internally as
// a convenience for callers that have not.
func IsReserved(s string) bool {
	_, ok := reserved[toLowerASCII(s)]
	return ok
}

// ReservedWords returns a sorted copy of the reserved-word list, for tests
// (such as tests/arch/routes_test.go) that cross-check it against the router.
func ReservedWords() []string {
	out := make([]string, 0, len(reserved))
	for w := range reserved {
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}

func toLowerASCII(s string) string {
	b := []byte(s)
	changed := false
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
			changed = true
		}
	}
	if !changed {
		return s
	}
	return string(b)
}
