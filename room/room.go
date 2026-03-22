package room

import (
	"fmt"
	"sync"
	"time"

	"github.com/your-org/nakama-voice/audio"
	"github.com/your-org/nakama-voice/config"
	"github.com/your-org/nakama-voice/signaling"
)

// Participant represents a user in a voice room.
type Participant struct {
	UserID    string
	SessionID string
	Username  string
	Role      signaling.PermissionRole
	Muted     bool
	Speaking  bool
	Position  *signaling.Position3D
	Channels  []string
	JoinedAt  time.Time
}

// Room represents a voice chat room containing participants and channels.
type Room struct {
	ID        string
	MatchID   string
	Label     string
	OwnerID   string
	CreatedAt time.Time
	Config    config.RoomDefaults

	// participants maps user ID to participant state.
	participants map[string]*Participant
	// channels manages all channels in this room.
	channels *ChannelManager
	// permissions manages role-based access control.
	permissions *PermissionChecker
	// spatial manages 3D position tracking (nil if disabled).
	spatial *SpatialTracker

	// vad maps user IDs to their VAD engine instance.
	vad map[string]*audio.VAD
	// effects maps user IDs to their audio effects chain.
	effects map[string]*audio.EffectsChain
	// decoders maps user IDs to their OPUS decoder for VAD/processing.
	decoders map[string]*audio.OPUSDecoder

	// recording holds the active recording session (nil if not recording).
	recording *RecordingSession

	// maxParticipants is the room capacity limit.
	maxParticipants int

	// mu protects participants, vad, effects, and recording.
	mu sync.RWMutex
}

// NewRoom creates a new voice room with the specified configuration.
func NewRoom(id, matchID, label, ownerID string, cfg config.RoomDefaults, audioCfg config.AudioConfig) *Room {
	r := &Room{
		ID:              id,
		MatchID:         matchID,
		Label:           label,
		OwnerID:         ownerID,
		CreatedAt:       time.Now(),
		Config:          cfg,
		participants:    make(map[string]*Participant, cfg.MaxParticipants),
		channels:        NewChannelManager(false),
		permissions:     NewPermissionChecker(ownerID),
		vad:             make(map[string]*audio.VAD, cfg.MaxParticipants),
		effects:         make(map[string]*audio.EffectsChain, cfg.MaxParticipants),
		decoders:        make(map[string]*audio.OPUSDecoder, cfg.MaxParticipants),
		maxParticipants: cfg.MaxParticipants,
	}

	// Create default channel
	defaultType := signaling.ChannelNonPositional
	if cfg.DefaultChannelType == "positional" {
		defaultType = signaling.ChannelPositional
	}
	_, _ = r.channels.CreateChannel("default", "Default", defaultType, 0)

	// Set up spatial audio if enabled
	if audioCfg.SpatialEnabled {
		model := audio.AttenuationLogarithmic
		switch audioCfg.AttenuationModel {
		case "linear":
			model = audio.AttenuationLinear
		case "inverse_square":
			model = audio.AttenuationInverseSquare
		}
		r.spatial = NewSpatialTracker(model, audioCfg.MaxSpatialDistance, audioCfg.HRTFEnabled)
	}

	return r
}

// AddParticipant adds a user to the room. Returns an error if the room is
// full or the user is already present.
func (r *Room) AddParticipant(userID, sessionID, username string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.participants[userID]; exists {
		return fmt.Errorf("room: user %s already in room %s", userID, r.ID)
	}

	if len(r.participants) >= r.maxParticipants {
		return fmt.Errorf("room: room %s is full (%d/%d)", r.ID, len(r.participants), r.maxParticipants)
	}

	role := signaling.RoleUser
	if userID == r.OwnerID {
		role = signaling.RoleOwner
	}

	r.participants[userID] = &Participant{
		UserID:    userID,
		SessionID: sessionID,
		Username:  username,
		Role:      role,
		Channels:  []string{"default"},
		JoinedAt:  time.Now(),
	}

	// Create VAD, effects chain, and OPUS decoder for this user
	r.vad[userID] = audio.NewVAD(audio.VADAggressive, 300)
	r.effects[userID] = audio.NewEffectsChain(48000)
	dec, decErr := audio.NewOPUSDecoder(48000, 2)
	if decErr == nil {
		r.decoders[userID] = dec
	}

	// Add to default channel
	r.channels.JoinUserToChannel(userID, "default")

	return nil
}

// RemoveParticipant removes a user from the room.
func (r *Room) RemoveParticipant(userID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.participants, userID)
	delete(r.vad, userID)
	delete(r.effects, userID)
	delete(r.decoders, userID)

	r.channels.RemoveUserFromAllChannels(userID)

	if r.spatial != nil {
		r.spatial.RemoveUser(userID)
	}
}

// GetParticipant returns a participant by user ID.
func (r *Room) GetParticipant(userID string) *Participant {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p := r.participants[userID]
	return p
}

// HasParticipant returns true if the user is in the room.
func (r *Room) HasParticipant(userID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, exists := r.participants[userID]
	return exists
}

// ParticipantCount returns the number of participants.
func (r *Room) ParticipantCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.participants)
}

// ParticipantList returns info for all participants.
func (r *Room) ParticipantList() []signaling.ParticipantInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	list := make([]signaling.ParticipantInfo, 0, len(r.participants))
	for _, p := range r.participants {
		list = append(list, signaling.ParticipantInfo{
			UserID:    p.UserID,
			SessionID: p.SessionID,
			Username:  p.Username,
			Role:      p.Role,
			Muted:     p.Muted,
			Speaking:  p.Speaking,
			Position:  p.Position,
			Channels:  p.Channels,
		})
	}
	return list
}

// ParticipantIDs returns all participant user IDs.
func (r *Room) ParticipantIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.participants))
	for uid := range r.participants {
		ids = append(ids, uid)
	}
	return ids
}

// SetMuted sets the mute state for a participant.
func (r *Room) SetMuted(userID string, muted bool) {
	r.mu.Lock()
	if p, ok := r.participants[userID]; ok {
		p.Muted = muted
	}
	if ec, ok := r.effects[userID]; ok {
		ec.SetMuted(muted)
	}
	r.mu.Unlock()
}

// SetSpeaking sets the speaking state for a participant.
func (r *Room) SetSpeaking(userID string, speaking bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.participants[userID]; ok {
		p.Speaking = speaking
	}
}

// IsEmpty returns true if the room has no participants.
func (r *Room) IsEmpty() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.participants) == 0
}

// Channels returns the channel manager.
func (r *Room) Channels() *ChannelManager {
	return r.channels
}

// Permissions returns the permission checker.
func (r *Room) Permissions() *PermissionChecker {
	return r.permissions
}

// Spatial returns the spatial tracker (may be nil if spatial audio is disabled).
func (r *Room) Spatial() *SpatialTracker {
	return r.spatial
}

// GetVAD returns the VAD engine for a user (nil if not found).
func (r *Room) GetVAD(userID string) *audio.VAD {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.vad[userID]
}

// GetEffects returns the effects chain for a user (nil if not found).
func (r *Room) GetEffects(userID string) *audio.EffectsChain {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.effects[userID]
}

// GetDecoder returns the OPUS decoder for a user (nil if not found).
func (r *Room) GetDecoder(userID string) *audio.OPUSDecoder {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.decoders[userID]
}

// RoomState builds a full room state message for a joining client.
func (r *Room) RoomState(iceServers []signaling.IceServerInfo) signaling.RoomStateMsg {
	return signaling.RoomStateMsg{
		RoomID:       r.ID,
		Participants: r.ParticipantList(),
		Channels:     r.channels.AllChannels(),
		IceServers:   iceServers,
		Config: signaling.RoomConfigMsg{
			MaxParticipants:    r.maxParticipants,
			DefaultChannelType: r.Config.DefaultChannelType,
			AutoRecording:      r.Config.AutoRecording,
			SpatialEnabled:     r.spatial != nil,
		},
	}
}