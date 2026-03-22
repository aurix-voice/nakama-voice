package nakama

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
)

// Notification codes for voice events.
const (
	NotifVoiceJoin         = 100
	NotifVoiceLeave        = 101
	NotifVoiceMute         = 102
	NotifVoiceKick         = 103
	NotifVoiceBan          = 104
	NotifVoiceRecordStart  = 105
	NotifVoiceRecordStop   = 106
)

// Notifier sends Nakama notifications for voice lifecycle events.
type Notifier struct{}

// NewNotifier creates a voice event notifier.
func NewNotifier() *Notifier {
	return &Notifier{}
}

// NotifyJoin sends a notification when a user joins a voice room.
func (n *Notifier) NotifyJoin(ctx context.Context, nk runtime.NakamaModule, roomID, userID, username string) error {
	content := map[string]interface{}{
		"room_id":  roomID,
		"user_id":  userID,
		"username": username,
		"event":    "join",
	}
	return n.send(ctx, nk, userID, NotifVoiceJoin, "Voice room joined", content)
}

// NotifyLeave sends a notification when a user leaves a voice room.
func (n *Notifier) NotifyLeave(ctx context.Context, nk runtime.NakamaModule, roomID, userID string) error {
	content := map[string]interface{}{
		"room_id": roomID,
		"user_id": userID,
		"event":   "leave",
	}
	return n.send(ctx, nk, userID, NotifVoiceLeave, "Voice room left", content)
}

// NotifyMute sends a notification when a user is muted.
func (n *Notifier) NotifyMute(ctx context.Context, nk runtime.NakamaModule, roomID, userID string, muted bool) error {
	content := map[string]interface{}{
		"room_id": roomID,
		"user_id": userID,
		"muted":   muted,
		"event":   "mute",
	}
	return n.send(ctx, nk, userID, NotifVoiceMute, "Voice mute state changed", content)
}

// NotifyKick sends a notification when a user is kicked.
func (n *Notifier) NotifyKick(ctx context.Context, nk runtime.NakamaModule, roomID, userID, reason string, cooldown int) error {
	content := map[string]interface{}{
		"room_id":          roomID,
		"user_id":          userID,
		"reason":           reason,
		"cooldown_seconds": cooldown,
		"event":            "kick",
	}
	return n.send(ctx, nk, userID, NotifVoiceKick, "Kicked from voice room", content)
}

// NotifyBan sends a notification when a user is banned.
func (n *Notifier) NotifyBan(ctx context.Context, nk runtime.NakamaModule, roomID, userID, reason string, duration int64) error {
	content := map[string]interface{}{
		"room_id":          roomID,
		"user_id":          userID,
		"reason":           reason,
		"duration_seconds": duration,
		"event":            "ban",
	}
	return n.send(ctx, nk, userID, NotifVoiceBan, "Banned from voice room", content)
}

// NotifyRecordingStart sends a notification when recording begins.
func (n *Notifier) NotifyRecordingStart(ctx context.Context, nk runtime.NakamaModule, roomID, recordingID string, userIDs []string) error {
	content := map[string]interface{}{
		"room_id":      roomID,
		"recording_id": recordingID,
		"event":        "recording_start",
	}
	for _, uid := range userIDs {
		if err := n.send(ctx, nk, uid, NotifVoiceRecordStart, "Recording started", content); err != nil {
			return err
		}
	}
	return nil
}

// NotifyRecordingStop sends a notification when recording stops.
func (n *Notifier) NotifyRecordingStop(ctx context.Context, nk runtime.NakamaModule, roomID, recordingID string, userIDs []string) error {
	content := map[string]interface{}{
		"room_id":      roomID,
		"recording_id": recordingID,
		"event":        "recording_stop",
	}
	for _, uid := range userIDs {
		if err := n.send(ctx, nk, uid, NotifVoiceRecordStop, "Recording stopped", content); err != nil {
			return err
		}
	}
	return nil
}

func (n *Notifier) send(ctx context.Context, nk runtime.NakamaModule, userID string, code int, subject string, content map[string]interface{}) error {
	data, err := json.Marshal(content)
	if err != nil {
		return fmt.Errorf("notification: marshal: %w", err)
	}

	if err := nk.NotificationSend(ctx, userID, subject, string(data), code, "", true); err != nil {
		return fmt.Errorf("notification: send to %s: %w", userID, err)
	}

	return nil
}
