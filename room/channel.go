package room

import (
	"fmt"
	"sync"

	"github.com/your-org/nakama-voice/signaling"
)

// Channel represents a communication pathway within a voice room.
// Participants can be members of multiple channels simultaneously.
type Channel struct {
	ID       string
	Label    string
	Type     signaling.ChannelType
	Priority int // Lower number = higher priority

	// members maps user IDs to membership state.
	members map[string]bool
	// pttState maps user IDs to their push-to-talk state (true = pressed).
	pttState map[string]bool

	// duckingFactor is the gain reduction applied to lower-priority channels
	// when this channel is active (0.0 to 1.0).
	duckingFactor float64

	// mu protects members, pttState.
	mu sync.RWMutex
}

// NewChannel creates a new channel with the specified properties.
func NewChannel(id, label string, channelType signaling.ChannelType, priority int) *Channel {
	return &Channel{
		ID:            id,
		Label:         label,
		Type:          channelType,
		Priority:      priority,
		members:       make(map[string]bool, 16),
		pttState:      make(map[string]bool, 16),
		duckingFactor: 0.3,
	}
}

// AddMember adds a user to the channel.
func (c *Channel) AddMember(userID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.members[userID] = true
	c.pttState[userID] = false
}

// RemoveMember removes a user from the channel.
func (c *Channel) RemoveMember(userID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.members, userID)
	delete(c.pttState, userID)
}

// HasMember returns true if the user is a member of this channel.
func (c *Channel) HasMember(userID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.members[userID]
}

// Members returns a copy of the member list.
func (c *Channel) Members() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	members := make([]string, 0, len(c.members))
	for uid := range c.members {
		members = append(members, uid)
	}
	return members
}

// MemberCount returns the number of members.
func (c *Channel) MemberCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.members)
}

// SetPTTState sets the push-to-talk state for a user.
func (c *Channel) SetPTTState(userID string, pressed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pttState[userID] = pressed
}

// IsPTTActive returns whether a user's push-to-talk is active.
func (c *Channel) IsPTTActive(userID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.pttState[userID]
}

// Info returns a ChannelInfo struct for signaling.
func (c *Channel) Info() signaling.ChannelInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	members := make([]string, 0, len(c.members))
	for uid := range c.members {
		members = append(members, uid)
	}
	return signaling.ChannelInfo{
		ID:       c.ID,
		Label:    c.Label,
		Type:     c.Type,
		Priority: c.Priority,
		Members:  members,
	}
}

// ChannelManager manages channels within a room and handles priority-based
// audio ducking and push-to-talk gating.
type ChannelManager struct {
	// channels maps channel ID to Channel.
	channels map[string]*Channel
	// pushToTalkEnabled indicates whether PTT mode is enforced.
	pushToTalkEnabled bool

	// mu protects channels map.
	mu sync.RWMutex
}

// NewChannelManager creates a channel manager with an optional default channel.
func NewChannelManager(pushToTalk bool) *ChannelManager {
	return &ChannelManager{
		channels:          make(map[string]*Channel, 8),
		pushToTalkEnabled: pushToTalk,
	}
}

// CreateChannel creates a new channel and adds it to the manager.
func (cm *ChannelManager) CreateChannel(id, label string, channelType signaling.ChannelType, priority int) (*Channel, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if _, exists := cm.channels[id]; exists {
		return nil, fmt.Errorf("channel: %s already exists", id)
	}

	ch := NewChannel(id, label, channelType, priority)
	cm.channels[id] = ch
	return ch, nil
}

// GetChannel returns a channel by ID.
func (cm *ChannelManager) GetChannel(id string) *Channel {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.channels[id]
}

// RemoveChannel removes a channel.
func (cm *ChannelManager) RemoveChannel(id string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	delete(cm.channels, id)
}

// AllChannels returns info for all channels.
func (cm *ChannelManager) AllChannels() []signaling.ChannelInfo {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	infos := make([]signaling.ChannelInfo, 0, len(cm.channels))
	for _, ch := range cm.channels {
		infos = append(infos, ch.Info())
	}
	return infos
}

// IsForwardingAllowed checks if audio from senderID should be forwarded to
// receiverID based on shared channel membership and PTT state.
func (cm *ChannelManager) IsForwardingAllowed(senderID, receiverID string) bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	for _, ch := range cm.channels {
		if !ch.HasMember(senderID) || !ch.HasMember(receiverID) {
			continue
		}

		// Echo channel: sender == receiver
		if ch.Type == signaling.ChannelEcho && senderID == receiverID {
			return true
		}

		// Directed channel handled by whisper logic separately
		if ch.Type == signaling.ChannelDirected {
			continue
		}

		// Check PTT if enabled
		if cm.pushToTalkEnabled {
			if !ch.IsPTTActive(senderID) {
				continue
			}
		}

		return true
	}

	return false
}

// GetDuckingFactor computes the effective ducking factor for audio from
// a sender to a receiver based on the highest-priority active channel.
func (cm *ChannelManager) GetDuckingFactor(senderID, receiverID string) float64 {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	highestPriority := 999
	for _, ch := range cm.channels {
		if ch.HasMember(senderID) && ch.HasMember(receiverID) {
			if ch.Priority < highestPriority {
				highestPriority = ch.Priority
			}
		}
	}

	if highestPriority == 999 {
		return 1.0
	}

	// Apply ducking: channels with priority higher (lower number) than the
	// highest shared channel get ducked
	factor := 1.0
	for _, ch := range cm.channels {
		if ch.HasMember(receiverID) && ch.Priority > highestPriority {
			factor *= ch.duckingFactor
		}
	}

	return factor
}

// JoinUserToChannel adds a user to a specific channel.
func (cm *ChannelManager) JoinUserToChannel(userID, channelID string) error {
	cm.mu.RLock()
	ch, ok := cm.channels[channelID]
	cm.mu.RUnlock()

	if !ok {
		return fmt.Errorf("channel: %s not found", channelID)
	}

	ch.AddMember(userID)
	return nil
}

// LeaveUserFromChannel removes a user from a specific channel.
func (cm *ChannelManager) LeaveUserFromChannel(userID, channelID string) error {
	cm.mu.RLock()
	ch, ok := cm.channels[channelID]
	cm.mu.RUnlock()

	if !ok {
		return fmt.Errorf("channel: %s not found", channelID)
	}

	ch.RemoveMember(userID)
	return nil
}

// RemoveUserFromAllChannels removes a user from all channels.
func (cm *ChannelManager) RemoveUserFromAllChannels(userID string) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	for _, ch := range cm.channels {
		ch.RemoveMember(userID)
	}
}