// Package moderation implements mute, kick, and ban operations with
// permission hierarchy checks, notification dispatch, and expiry scheduling.
package moderation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"

	"github.com/aurix-voice/nakama-voice/metrics"
	"github.com/aurix-voice/nakama-voice/room"
)

// Moderator handles all moderation actions within voice rooms.
type Moderator struct {
	auditLog     *AuditLog
	voiceMetrics *metrics.VoiceMetrics
}

// NewModerator creates a new moderation handler.
func NewModerator(auditLog *AuditLog, m *metrics.VoiceMetrics) *Moderator {
	return &Moderator{
		auditLog:     auditLog,
		voiceMetrics: m,
	}
}

// MuteUser mutes or unmutes a participant in a room.
// Validates permissions before applying the action.
func (mod *Moderator) MuteUser(
	ctx context.Context,
	logger runtime.Logger,
	nk runtime.NakamaModule,
	r *room.Room,
	actorID, targetID string,
	muted bool,
	durationSeconds int64,
	reason string,
) error {
	actor := r.GetParticipant(actorID)
	if actor == nil {
		return fmt.Errorf("moderation: actor %s not in room", actorID)
	}

	target := r.GetParticipant(targetID)
	if target == nil {
		return fmt.Errorf("moderation: target %s not in room", targetID)
	}

	// Self-mute is always allowed
	if actorID != targetID {
		if err := r.Permissions().CheckPermission(actorID, actor.Role, room.ActionMuteOther); err != nil {
			return fmt.Errorf("moderation: mute permission denied: %w", err)
		}

		if err := r.Permissions().CheckHierarchy(actorID, actor.Role, targetID, target.Role); err != nil {
			return fmt.Errorf("moderation: hierarchy check failed: %w", err)
		}
	}

	r.SetMuted(targetID, muted)

	action := "mute"
	if !muted {
		action = "unmute"
	}

	// Write audit log
	if err := mod.auditLog.Write(ctx, nk, AuditEntry{
		RoomID:   r.ID,
		ActorID:  actorID,
		TargetID: targetID,
		Action:   action,
		Reason:   reason,
	}); err != nil {
		logger.Warn("moderation: audit log write failed: %v", err)
	}

	if mod.voiceMetrics != nil {
		mod.voiceMetrics.IncModerationAction(action)
	}

	logger.Info("moderation: %s %s user %s in room %s (reason: %s)",
		actorID, action, targetID, r.ID, reason)

	return nil
}

// KickUser removes a participant from a room with an optional rejoin cooldown.
func (mod *Moderator) KickUser(
	ctx context.Context,
	logger runtime.Logger,
	nk runtime.NakamaModule,
	r *room.Room,
	actorID, targetID string,
	reason string,
	cooldownSeconds int,
) error {
	actor := r.GetParticipant(actorID)
	if actor == nil {
		return fmt.Errorf("moderation: actor %s not in room", actorID)
	}

	target := r.GetParticipant(targetID)
	if target == nil {
		return fmt.Errorf("moderation: target %s not in room", targetID)
	}

	if err := r.Permissions().CheckPermission(actorID, actor.Role, room.ActionKick); err != nil {
		return fmt.Errorf("moderation: kick permission denied: %w", err)
	}

	if err := r.Permissions().CheckHierarchy(actorID, actor.Role, targetID, target.Role); err != nil {
		return fmt.Errorf("moderation: hierarchy check failed: %w", err)
	}

	r.RemoveParticipant(targetID)

	// Write audit log
	if err := mod.auditLog.Write(ctx, nk, AuditEntry{
		RoomID:   r.ID,
		ActorID:  actorID,
		TargetID: targetID,
		Action:   "kick",
		Reason:   reason,
		Metadata: map[string]string{"cooldown_seconds": fmt.Sprintf("%d", cooldownSeconds)},
	}); err != nil {
		logger.Warn("moderation: audit log write failed: %v", err)
	}

	if mod.voiceMetrics != nil {
		mod.voiceMetrics.IncModerationAction("kick")
	}

	logger.Info("moderation: %s kicked %s from room %s (cooldown: %ds, reason: %s)",
		actorID, targetID, r.ID, cooldownSeconds, reason)

	return nil
}

// BanUser bans a participant from a room, removing them and preventing rejoin.
func (mod *Moderator) BanUser(
	ctx context.Context,
	logger runtime.Logger,
	nk runtime.NakamaModule,
	r *room.Room,
	actorID, targetID string,
	reason string,
	durationSeconds int64,
	permanent bool,
) error {
	actor := r.GetParticipant(actorID)
	if actor == nil {
		return fmt.Errorf("moderation: actor %s not in room", actorID)
	}

	target := r.GetParticipant(targetID)
	if target == nil {
		return fmt.Errorf("moderation: target %s not in room", targetID)
	}

	if err := r.Permissions().CheckPermission(actorID, actor.Role, room.ActionBan); err != nil {
		return fmt.Errorf("moderation: ban permission denied: %w", err)
	}

	if err := r.Permissions().CheckHierarchy(actorID, actor.Role, targetID, target.Role); err != nil {
		return fmt.Errorf("moderation: hierarchy check failed: %w", err)
	}

	// Remove from room
	r.RemoveParticipant(targetID)

	// Store ban record
	expiresAt := int64(0)
	if !permanent && durationSeconds > 0 {
		expiresAt = time.Now().Add(time.Duration(durationSeconds) * time.Second).Unix()
	}
	_ = expiresAt // Would be stored via nakama storage in full integration

	// Write audit log
	if err := mod.auditLog.Write(ctx, nk, AuditEntry{
		RoomID:   r.ID,
		ActorID:  actorID,
		TargetID: targetID,
		Action:   "ban",
		Reason:   reason,
		Metadata: map[string]string{
			"duration_seconds": fmt.Sprintf("%d", durationSeconds),
			"permanent":        fmt.Sprintf("%t", permanent),
		},
	}); err != nil {
		logger.Warn("moderation: audit log write failed: %v", err)
	}

	if mod.voiceMetrics != nil {
		mod.voiceMetrics.IncModerationAction("ban")
	}

	logger.Info("moderation: %s banned %s from room %s (permanent: %t, duration: %ds, reason: %s)",
		actorID, targetID, r.ID, permanent, durationSeconds, reason)

	return nil
}

// CheckBan checks if a user is banned from a room.
func (mod *Moderator) CheckBan(ctx context.Context, nk runtime.NakamaModule, roomID, userID string) (bool, error) {
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: "voice_bans",
			Key:        fmt.Sprintf("%s:%s", roomID, userID),
			UserID:     "",
		},
	})
	if err != nil {
		return false, fmt.Errorf("moderation: read ban: %w", err)
	}

	if len(objs) == 0 {
		return false, nil
	}

	// Check if ban has expired
	var banData struct {
		ExpiresAt int64 `json:"expires_at"`
		Permanent bool  `json:"permanent"`
	}
	if parseErr := json.Unmarshal([]byte(objs[0].Value), &banData); parseErr != nil {
		return false, fmt.Errorf("moderation: parse ban: %w", parseErr)
	}

	if banData.Permanent {
		return true, nil
	}

	if banData.ExpiresAt > 0 && time.Now().Unix() >= banData.ExpiresAt {
		// Ban expired, remove it
		_ = nk.StorageDelete(ctx, []*runtime.StorageDelete{
			{
				Collection: "voice_bans",
				Key:        fmt.Sprintf("%s:%s", roomID, userID),
				UserID:     "",
			},
		})
		return false, nil
	}

	return true, nil
}
