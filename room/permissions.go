// Package room implements voice room management including room lifecycle,
// channels, spatial tracking, permissions, and recording.
package room

import (
	"fmt"
	"sync"

	"github.com/aurix-voice/nakama-voice/signaling"
)

// Action represents a moderation or room management action that requires
// permission checking.
type Action int

const (
	// ActionSpeak permits audio transmission.
	ActionSpeak Action = iota
	// ActionMuteSelf permits muting oneself.
	ActionMuteSelf
	// ActionMuteOther permits muting another participant.
	ActionMuteOther
	// ActionKick permits removing a participant from the room.
	ActionKick
	// ActionBan permits banning a participant from the room.
	ActionBan
	// ActionUpdateConfig permits changing room configuration.
	ActionUpdateConfig
	// ActionStartRecording permits starting a recording.
	ActionStartRecording
	// ActionStopRecording permits stopping a recording.
	ActionStopRecording
	// ActionDeleteRoom permits deleting the room.
	ActionDeleteRoom
	// ActionViewParticipants permits viewing the participant list.
	ActionViewParticipants
)

// PermissionMatrix defines which roles can perform which actions.
// The matrix is indexed by [role][action].
var PermissionMatrix = map[signaling.PermissionRole]map[Action]bool{
	signaling.RoleUser: {
		ActionSpeak:            true,
		ActionMuteSelf:         true,
		ActionMuteOther:        false,
		ActionKick:             false,
		ActionBan:              false,
		ActionUpdateConfig:     false,
		ActionStartRecording:   false,
		ActionStopRecording:    false,
		ActionDeleteRoom:       false,
		ActionViewParticipants: true,
	},
	signaling.RoleModerator: {
		ActionSpeak:            true,
		ActionMuteSelf:         true,
		ActionMuteOther:        true,
		ActionKick:             true,
		ActionBan:              false,
		ActionUpdateConfig:     false,
		ActionStartRecording:   false,
		ActionStopRecording:    false,
		ActionDeleteRoom:       false,
		ActionViewParticipants: true,
	},
	signaling.RoleAdmin: {
		ActionSpeak:            true,
		ActionMuteSelf:         true,
		ActionMuteOther:        true,
		ActionKick:             true,
		ActionBan:              true,
		ActionUpdateConfig:     true,
		ActionStartRecording:   true,
		ActionStopRecording:    true,
		ActionDeleteRoom:       false,
		ActionViewParticipants: true,
	},
	signaling.RoleOwner: {
		ActionSpeak:            true,
		ActionMuteSelf:         true,
		ActionMuteOther:        true,
		ActionKick:             true,
		ActionBan:              true,
		ActionUpdateConfig:     true,
		ActionStartRecording:   true,
		ActionStopRecording:    true,
		ActionDeleteRoom:       true,
		ActionViewParticipants: true,
	},
}

// PermissionChecker validates whether a participant has permission to
// perform specific actions within a room.
type PermissionChecker struct {
	// roleOverrides allows per-room role overrides for specific users.
	roleOverrides map[string]signaling.PermissionRole
	// ownerID is the user ID of the room owner.
	ownerID string

	// mu protects roleOverrides.
	mu sync.RWMutex
}

// NewPermissionChecker creates a permission checker for a room with the
// specified owner.
func NewPermissionChecker(ownerID string) *PermissionChecker {
	return &PermissionChecker{
		roleOverrides: make(map[string]signaling.PermissionRole, 8),
		ownerID:       ownerID,
	}
}

// CheckPermission verifies that the actor has permission to perform the
// specified action. Returns nil if allowed, or an error describing why not.
func (pc *PermissionChecker) CheckPermission(actorID string, actorRole signaling.PermissionRole, action Action) error {
	// Owner always has all permissions
	if actorID == pc.ownerID {
		return nil
	}

	// Check for role override
	pc.mu.RLock()
	if override, ok := pc.roleOverrides[actorID]; ok {
		actorRole = override
	}
	pc.mu.RUnlock()

	perms, ok := PermissionMatrix[actorRole]
	if !ok {
		return fmt.Errorf("permissions: unknown role %d", actorRole)
	}

	if allowed, exists := perms[action]; exists && allowed {
		return nil
	}

	return fmt.Errorf("permissions: role %d cannot perform action %d", actorRole, action)
}

// CheckHierarchy verifies that the actor's role is higher than the target's
// role for moderation actions. Returns an error if the actor cannot act on
// the target.
func (pc *PermissionChecker) CheckHierarchy(actorID string, actorRole signaling.PermissionRole, targetID string, targetRole signaling.PermissionRole) error {
	if actorID == pc.ownerID {
		return nil
	}
	if targetID == pc.ownerID {
		return fmt.Errorf("permissions: cannot perform action on room owner")
	}

	pc.mu.RLock()
	if override, ok := pc.roleOverrides[actorID]; ok {
		actorRole = override
	}
	if override, ok := pc.roleOverrides[targetID]; ok {
		targetRole = override
	}
	pc.mu.RUnlock()

	if actorRole <= targetRole {
		return fmt.Errorf("permissions: actor role %d not higher than target role %d", actorRole, targetRole)
	}

	return nil
}

// SetRole sets a role override for a specific user.
func (pc *PermissionChecker) SetRole(userID string, role signaling.PermissionRole) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.roleOverrides[userID] = role
}

// GetRole returns the effective role for a user. Returns the override
// if set, otherwise returns the provided default role.
func (pc *PermissionChecker) GetRole(userID string, defaultRole signaling.PermissionRole) signaling.PermissionRole {
	if userID == pc.ownerID {
		return signaling.RoleOwner
	}
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	if override, ok := pc.roleOverrides[userID]; ok {
		return override
	}
	return defaultRole
}

// RemoveRole removes a role override for a user.
func (pc *PermissionChecker) RemoveRole(userID string) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	delete(pc.roleOverrides, userID)
}
