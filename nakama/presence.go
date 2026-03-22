package nakama

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
)

// PresenceManager reflects voice room membership in Nakama's presence streams.
type PresenceManager struct{}

// NewPresenceManager creates a presence manager.
func NewPresenceManager() *PresenceManager {
	return &PresenceManager{}
}

// JoinPresence adds a user's presence to the voice room stream.
func (pm *PresenceManager) JoinPresence(ctx context.Context, nk runtime.NakamaModule, roomID, userID, sessionID, username string) error {
	streamMode := uint8(123) // Custom stream mode for voice
	streamSubject := roomID
	streamSubcontext := ""
	streamLabel := "voice"

	meta := map[string]string{
		"room_id": roomID,
		"voice":   "true",
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("presence: marshal meta: %w", err)
	}

	if err := nk.StreamUserJoin(streamMode, streamSubject, streamSubcontext, streamLabel, userID, sessionID, false, false, string(metaJSON)); err != nil {
		return fmt.Errorf("presence: join stream: %w", err)
	}

	return nil
}

// LeavePresence removes a user's presence from the voice room stream.
func (pm *PresenceManager) LeavePresence(ctx context.Context, nk runtime.NakamaModule, roomID, userID, sessionID string) error {
	streamMode := uint8(123)
	streamSubject := roomID
	streamSubcontext := ""
	streamLabel := "voice"

	if err := nk.StreamUserLeave(streamMode, streamSubject, streamSubcontext, streamLabel, userID, sessionID); err != nil {
		return fmt.Errorf("presence: leave stream: %w", err)
	}

	return nil
}

// GetRoomPresences returns all presences in a voice room stream.
func (pm *PresenceManager) GetRoomPresences(ctx context.Context, nk runtime.NakamaModule, roomID string) ([]runtime.Presence, error) {
	streamMode := uint8(123)
	streamSubject := roomID
	streamSubcontext := ""
	streamLabel := "voice"

	presences, err := nk.StreamUserList(streamMode, streamSubject, streamSubcontext, streamLabel, true, true)
	if err != nil {
		return nil, fmt.Errorf("presence: list stream: %w", err)
	}

	return presences, nil
}