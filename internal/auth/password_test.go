package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordRoundTrip(t *testing.T) {
	phc, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(phc, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("PHC string does not encode the expected parameters: %q", phc)
	}
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[4] == "" || parts[5] == "" {
		t.Fatalf("PHC string has unexpected shape: %q", phc)
	}
	ok, err := VerifyPassword(phc, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("correct password rejected: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword(phc, "incorrect horse battery staple")
	if err != nil || ok {
		t.Fatalf("wrong password accepted: ok=%v err=%v", ok, err)
	}
}

func TestPasswordSaltIsFresh(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Fatal("two hashes of the same password must differ (fresh salt)")
	}
}

func TestPasswordVerifyRejectsMalformed(t *testing.T) {
	for _, bad := range []string{
		"",
		"$argon2i$v=19$m=65536,t=3,p=4$AAAA$BBBB",
		"$argon2id$v=19$m=65536,t=3$AAAA$BBBB",
		"$argon2id$v=19$m=65536,t=3,p=4$not*base64$BBBB",
		"plain",
	} {
		if ok, err := VerifyPassword(bad, "x"); ok || err == nil {
			t.Errorf("VerifyPassword(%q) = ok=%v err=%v; want rejection with error", bad, ok, err)
		}
	}
}

// TestPasswordVerifyTimingUniform pins that a wrong password costs the same Argon2 work
// as a right one: the KDF always runs to completion and only then compares.
func TestPasswordVerifyTimingUniform(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test skipped in -short mode")
	}
	phc, err := HashPassword("right")
	if err != nil {
		t.Fatal(err)
	}
	const rounds = 3
	timeIt := func(pw string) time.Duration {
		var best time.Duration
		for i := 0; i < rounds; i++ {
			start := time.Now()
			if _, err := VerifyPassword(phc, pw); err != nil {
				t.Fatal(err)
			}
			d := time.Since(start)
			if best == 0 || d < best {
				best = d
			}
		}
		return best
	}
	right := timeIt("right")
	wrong := timeIt("wrong")
	if wrong*2 < right {
		t.Fatalf("wrong-password verification measurably faster: right=%v wrong=%v", right, wrong)
	}
}
