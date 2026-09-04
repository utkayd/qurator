package auth

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/store"
)

// Bootstrap creates the one administrative account on first start (FR-032). It acts only
// when the store holds zero users AND both email and password are configured; it never
// recreates, resets, or consults a marker file. created reports whether it acted.
func Bootstrap(ctx context.Context, st store.Store, email, password string) (created bool, err error) {
	n, err := st.CountUsers(ctx)
	if err != nil {
		return false, fmt.Errorf("auth: bootstrap: count users: %w", err)
	}
	if n > 0 {
		return false, nil
	}
	if email == "" || password == "" {
		slog.Warn("auth: no users exist and no bootstrap credentials are configured; nobody can sign in")
		return false, nil
	}
	phc, err := HashPassword(password)
	if err != nil {
		return false, fmt.Errorf("auth: bootstrap: %w", err)
	}
	u := &domain.User{
		ID:           newID("usr_"),
		Email:        email,
		PasswordHash: phc,
		IsAdmin:      true,
		Source:       domain.UserSourceLocal,
		CreatedAt:    time.Now().UTC(),
	}
	if err := st.CreateUser(ctx, u); err != nil {
		return false, fmt.Errorf("auth: bootstrap: create admin: %w", err)
	}
	slog.Info("auth: bootstrap admin created", "user_id", u.ID)
	return true, nil
}
