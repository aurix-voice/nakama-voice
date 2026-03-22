package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// VoiceSession represents a user's voice session state persisted to Nakama storage.
type VoiceSession struct {
	UserID    string `json:"user_id"`
	SessionID string `json:"session_id"`
	RoomID    string `json:"room_id"`
	JoinedAt  int64  `json:"joined_at"`
	LastActive int64 `json:"last_active"`
	Muted     bool   `json:"muted"`
}

// SessionStore manages voice session state persistence.
type SessionStore struct{}

// NewSessionStore creates a session store.
func NewSessionStore() *SessionStore {
	return &SessionStore{}
}

// WriteSession persists a voice session to Nakama storage.
func (ss *SessionStore) WriteSession(ctx context.Context, nk runtime.NakamaModule, session VoiceSession) error {
	session.LastActive = time.Now().UnixMilli()

	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("session store: marshal: %w", err)
	}

	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      "voice_sessions",
			Key:             session.UserID,
			UserID:          session.UserID,
			Value:           string(data),
			PermissionRead:  1, // Owner read
			PermissionWrite: 0, // System write only
		},
	})
	if err != nil {
		return fmt.Errorf("session store: write: %w", err)
	}

	return nil
}

// ReadSession reads a user's voice session from storage.
func (ss *SessionStore) ReadSession(ctx context.Context, nk runtime.NakamaModule, userID string) (*VoiceSession, error) {
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: "voice_sessions",
			Key:        userID,
			UserID:     userID,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("session store: read: %w", err)
	}

	if len(objs) == 0 {
		return nil, nil
	}

	var session VoiceSession
	if parseErr := json.Unmarshal([]byte(objs[0].Value), &session); parseErr != nil {
		return nil, fmt.Errorf("session store: parse: %w", parseErr)
	}

	return &session, nil
}

// DeleteSession removes a user's voice session from storage.
func (ss *SessionStore) DeleteSession(ctx context.Context, nk runtime.NakamaModule, userID string) error {
	return nk.StorageDelete(ctx, []*runtime.StorageDelete{
		{
			Collection: "voice_sessions",
			Key:        userID,
			UserID:     userID,
		},
	})
}

// UpdateActivity updates the last active timestamp for a session.
func (ss *SessionStore) UpdateActivity(ctx context.Context, nk runtime.NakamaModule, userID string) error {
	session, err := ss.ReadSession(ctx, nk, userID)
	if err != nil {
		return fmt.Errorf("session store: read for update: %w", err)
	}
	if session == nil {
		return nil
	}
	session.LastActive = time.Now().UnixMilli()
	return ss.WriteSession(ctx, nk, *session)
}
