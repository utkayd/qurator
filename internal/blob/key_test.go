package blob

import "testing"

func TestValidateKey(t *testing.T) {
	ok := []string{"a", "codes/ab/cod_x.png", "logos/2026/09/x.jpg"}
	bad := []string{"", "/abs", "../x", "a/../b", "a/./b", "a//b", "a\\b", "a\x00b", "./a", "a/"}
	for _, k := range ok {
		if err := ValidateKey(k); err != nil {
			t.Errorf("ValidateKey(%q) = %v, want nil", k, err)
		}
	}
	for _, k := range bad {
		if err := ValidateKey(k); err == nil {
			t.Errorf("ValidateKey(%q) = nil, want ErrInvalidKey", k)
		}
	}
}
