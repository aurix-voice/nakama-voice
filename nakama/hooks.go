package nakama

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// Hooks registers before and after hooks for Nakama lifecycle events
// that interact with the voice module.
type Hooks struct {
	sessionStore *SessionStore
}

// SessionStore is a minimal interface matching storage.SessionStore.
type SessionStore interface {
	DeleteSession(ctx context.Context, nk runtime.NakamaModule, userID string) error
}

// NewHooks creates a voice hooks handler.
func NewHooks(sessionStore SessionStore) *Hooks {
	return &Hooks{sessionStore: sessionStore}
}

// BeforeAuthenticateCustom is called before custom authentication.
// It can be used to validate voice-specific auth tokens.
func (h *Hooks) BeforeAuthenticateCustom(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.AuthenticateCustomRequest) (*api.AuthenticateCustomRequest, error) {
	logger.Debug("voice hooks: before authenticate custom for id=%s", in.Account.Id)
	return in, nil
}

// AfterAuthenticateCustom is called after custom authentication.
func (h *Hooks) AfterAuthenticateCustom(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, out *api.Session, in *api.AuthenticateCustomRequest) error {
	logger.Debug("voice hooks: after authenticate custom for id=%s", in.Account.Id)
	return nil
}

// BeforeSessionLogout cleans up voice sessions before logout.
func (h *Hooks) BeforeSessionLogout(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.SessionLogoutRequest) (*api.SessionLogoutRequest, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if ok && userID != "" {
		if err := h.sessionStore.DeleteSession(ctx, nk, userID); err != nil {
			logger.Warn("voice hooks: failed to clean session on logout for %s: %v", userID, err)
		}
	}
	return in, nil
}

// RegisterHooks registers all voice-related hooks with Nakama.
func (h *Hooks) RegisterHooks(initializer runtime.Initializer) error {
	if err := initializer.RegisterBeforeAuthenticateCustom(h.BeforeAuthenticateCustom); err != nil {
		return fmt.Errorf("hooks: register before auth: %w", err)
	}

	if err := initializer.RegisterAfterAuthenticateCustom(h.AfterAuthenticateCustom); err != nil {
		return fmt.Errorf("hooks: register after auth: %w", err)
	}

	if err := initializer.RegisterBeforeSessionLogout(h.BeforeSessionLogout); err != nil {
		return fmt.Errorf("hooks: register before session logout: %w", err)
	}

	return nil
}