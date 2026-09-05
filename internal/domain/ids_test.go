package domain

import (
	"strings"
	"testing"
)

func TestNewIDShape(t *testing.T) {
	id := NewCodeID()
	if !strings.HasPrefix(id, "cod_") || len(id) != len("cod_")+idRandomLen {
		t.Fatalf("unexpected id %q", id)
	}
	for _, r := range id[len("cod_"):] {
		if !strings.ContainsRune(idAlphabet, r) {
			t.Fatalf("id %q contains %q outside alphabet", id, r)
		}
	}
	for _, banned := range "ilouILOU" {
		if strings.ContainsRune(idAlphabet, banned) {
			t.Fatalf("alphabet must exclude %q", banned)
		}
	}
}

func TestNewIDUniqueness(t *testing.T) {
	const n = 100_000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := NewID("t")
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id after %d draws: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}
