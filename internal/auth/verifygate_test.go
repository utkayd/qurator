package auth

import (
	"context"
	"errors"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/store"
	"sync"
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/store/storetest"
)

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
	releases[0]()
	r, ok := a.AcquireVerifySlot()
	if !ok {
		t.Fatal("released slot must be available")
	}
	defer r()
	releases[0]()
	if _, ok := a.AcquireVerifySlot(); ok {
		t.Fatal("double release must not widen the cap")
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

type pausedUserStore struct {
	store.Store
	once   sync.Once
	read   chan struct{}
	resume chan struct{}
}

func (s *pausedUserStore) GetUserByID(ctx context.Context, id string) (*domain.User, error) {
	u, err := s.Store.GetUserByID(ctx, id)
	s.once.Do(func() {
		close(s.read)
		<-s.resume
	})
	return u, err
}

func TestRevokeSessionsPreventsStaleCacheFill(t *testing.T) {
	a, st, _ := newTestAuth(t)
	u := seedUser(t, st, "race@example.com", false)
	tok, _, err := a.IssueSession(u)
	if err != nil {
		t.Fatal(err)
	}
	paused := &pausedUserStore{Store: st, read: make(chan struct{}), resume: make(chan struct{})}
	a.store = paused
	var resume sync.Once
	unblock := func() { resume.Do(func() { close(paused.resume) }) }
	defer unblock()
	done := make(chan error, 1)
	go func() { _, err := a.userByID(t.Context(), u.ID); done <- err }()
	<-paused.read
	if err := a.RevokeSessions(t.Context(), u.ID); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := a.verifySession(t.Context(), tok); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("session after concurrent revocation: %v, want unauthorized", err)
	}
}
