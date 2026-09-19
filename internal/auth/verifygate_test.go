package auth

import (
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/store/storetest"
)

// TestVerifySlotsCapConcurrency pins the process-wide Argon2id cap (F15): an Authenticator
// hands out at most VerifySlots verification slots at once, never blocks a caller when
// they are all taken, and frees a slot when its release is called.
func TestVerifySlotsCapConcurrency(t *testing.T) {
	st := storetest.NewMemStore()
	a, err := New(st, AuthOptions{DevMode: true, VerifySlots: 2}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	r1, ok := a.AcquireVerifySlot()
	if !ok {
		t.Fatal("slot 1 must be free")
	}
	r2, ok := a.AcquireVerifySlot()
	if !ok {
		t.Fatal("slot 2 must be free")
	}
	if _, ok := a.AcquireVerifySlot(); ok {
		t.Fatal("slot 3 must be refused: cap is 2")
	}
	r1()
	r3, ok := a.AcquireVerifySlot()
	if !ok {
		t.Fatal("slot must be free again after release")
	}
	// Releasing twice must not open an extra slot.
	r1()
	if _, ok := a.AcquireVerifySlot(); ok {
		t.Fatal("double release must not widen the cap")
	}
	r2()
	r3()
}

// TestVerifySlotsDefault pins the default cap so a zero option does not mean "unbounded".
func TestVerifySlotsDefault(t *testing.T) {
	st := storetest.NewMemStore()
	a, err := New(st, AuthOptions{DevMode: true}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	var releases []func()
	for i := 0; i < DefaultVerifySlots; i++ {
		r, ok := a.AcquireVerifySlot()
		if !ok {
			t.Fatalf("slot %d must be free under the default cap of %d", i+1, DefaultVerifySlots)
		}
		releases = append(releases, r)
	}
	if _, ok := a.AcquireVerifySlot(); ok {
		t.Fatalf("slot %d must be refused under the default cap", DefaultVerifySlots+1)
	}
	for _, r := range releases {
		r()
	}
}

// TestRevokeSessionsBumpsVersionAndEvictsCache pins that RevokeSessions invalidates a
// session immediately in this process: the positive user cache must not keep serving the
// pre-bump token_version for a cache TTL (F04).
func TestRevokeSessionsBumpsVersionAndEvictsCache(t *testing.T) {
	a, st, _ := newTestAuth(t)
	u := seedUser(t, st, "revoke@example.com", false)
	ctx := t.Context()
	tok, _, err := a.IssueSession(u)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.verifySession(ctx, tok); err != nil {
		t.Fatalf("fresh session: %v", err)
	}
	if err := a.RevokeSessions(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.verifySession(ctx, tok); err == nil {
		t.Fatal("session still verifies after RevokeSessions")
	}
	if err := a.RevokeSessions(ctx, "usr_doesnotexist"); err == nil {
		t.Fatal("RevokeSessions for an unknown user must fail")
	}
}
