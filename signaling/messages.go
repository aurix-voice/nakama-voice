// Package signaling implements the voice chat signaling protocol over Nakama's
// WebSocket connection using custom opcodes in the range 1000-1099.
package signaling

// Signaling opcodes for voice chat messages.
const (
	OpRoomJoinRequest   int64 = 1000
	OpSDPOffer          int64 = 1001
	OpSDPAnswer         int64 = 1002
	OpICECandidateLocal int64 = 1003
	OpICECandidateRemote int64 = 1004
	OpRoomLeave         int64 = 1005
	OpRoomState         int64 = 1006
	OpParticipantJoined int64 = 1007
	OpParticipantLeft   int64 = 1008
	OpMuteState         int64 = 1009
	OpPTTState          int64 = 1010
	OpVADState          int64 = 1011
	OpPositionUpdate    int64 = 1012
	OpSpatialParams     int64 = 1013
	OpChannelJoin       int64 = 1014
	OpChannelLeave      int64 = 1015
	OpKickNotify        int64 = 1016
	OpBanNotify         int64 = 1017
	OpError             int64 = 1018
	OpRoomConfigUpdate  int64 = 1019
	OpRecordingState    int64 = 1020
	OpPing              int64 = 1021
	OpPong              int64 = 1022
	OpModerationAction  int64 = 1023
	OpWhisper           int64 = 1024
	OpChannelState      int64 = 1025
)

// Error codes for voice chat signaling errors (range 4000-4099).
const (
	ErrUnknown           = 4000
	ErrRoomNotFound      = 4001
	ErrRoomFull          = 4002
	ErrPermissionDenied  = 4003
	ErrAlreadyInRoom     = 4004
	ErrNotInRoom         = 4005
	ErrInvalidSDP        = 4006
	ErrInvalidICE        = 4007
	ErrPeerFailed        = 4008
	ErrBanned            = 4009
	ErrMuted             = 4010
	ErrChannelNotFound   = 4011
	ErrInvalidPosition   = 4012
	ErrRecordingFailed   = 4013
	ErrConfigInvalid     = 4014
	ErrRateLimited       = 4015
	ErrInvalidPayload    = 4016
	ErrInternal          = 4017
	ErrSessionExpired    = 4018
	ErrCooldownActive    = 4019
)

// ChannelType represents the type of a voice channel.
type ChannelType int

const (
	// ChannelNonPositional is a standard party/team channel where all participants hear equally.
	ChannelNonPositional ChannelType = iota
	// ChannelPositional uses 3D spatial audio based on world coordinates.
	ChannelPositional
	// ChannelDirected is a whisper channel to specific users.
	ChannelDirected
	// ChannelEcho is a self-monitoring channel for testing.
	ChannelEcho
	// ChannelCrossParty is a cross-match channel.
	ChannelCrossParty
)

// PermissionRole represents a participant's permission level.
type PermissionRole int

const (
	// RoleUser has basic participant permissions.
	RoleUser PermissionRole = iota
	// RoleModerator can mute and kick users.
	RoleModerator
	// RoleAdmin can ban users and update room config.
	RoleAdmin
	// RoleOwner has full control over the room.
	RoleOwner
)

// Position3D represents a 3D position with orientation.
type Position3D struct {
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Z     float64 `json:"z"`
	Yaw   float64 `json:"yaw"`
	Pitch float64 `json:"pitch"`
}

// IceServerInfo describes an ICE server for WebRTC connectivity.
type IceServerInfo struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// ParticipantInfo describes a participant in signaling messages.
type ParticipantInfo struct {
	UserID    string         `json:"user_id"`
	SessionID string        `json:"session_id"`
	Username  string         `json:"username"`
	Role      PermissionRole `json:"role"`
	Muted     bool           `json:"muted"`
	Speaking  bool           `json:"speaking"`
	Position  *Position3D    `json:"position,omitempty"`
	Channels  []string       `json:"channels"`
}

// ChannelInfo describes a channel in signaling messages.
type ChannelInfo struct {
	ID       string      `json:"id"`
	Label    string      `json:"label"`
	Type     ChannelType `json:"type"`
	Priority int         `json:"priority"`
	Members  []string    `json:"members"`
}

// RoomConfigMsg holds room configuration for signaling.
type RoomConfigMsg struct {
	MaxParticipants    int     `json:"max_participants"`
	DefaultChannelType string  `json:"default_channel_type"`
	AutoRecording      bool    `json:"auto_recording"`
	SpatialEnabled     bool    `json:"spatial_enabled"`
	AttenuationModel   string  `json:"attenuation_model"`
	MaxDistance        float64 `json:"max_distance"`
	PushToTalk         bool    `json:"push_to_talk"`
}

// --- Signaling Message Payloads ---

// RoomJoinRequestMsg is sent by clients to join a voice room.
type RoomJoinRequestMsg struct {
	RoomID   string      `json:"room_id"`
	Channels []string    `json:"channels,omitempty"`
	Position *Position3D `json:"position,omitempty"`
}

// SDPMsg carries SDP offer or answer.
type SDPMsg struct {
	RoomID string `json:"room_id"`
	SDP    string `json:"sdp"`
	Type   string `json:"type"`
}

// ICECandidateMsg carries an ICE candidate.
type ICECandidateMsg struct {
	RoomID        string `json:"room_id"`
	Candidate     string `json:"candidate"`
	SDPMid        string `json:"sdp_mid"`
	SDPMLineIndex int    `json:"sdp_mline_index"`
}

// RoomLeaveMsg is sent by clients to leave a room.
type RoomLeaveMsg struct {
	RoomID string `json:"room_id"`
}

// RoomStateMsg is sent by the server with full room state.
type RoomStateMsg struct {
	RoomID       string            `json:"room_id"`
	Participants []ParticipantInfo `json:"participants"`
	Channels     []ChannelInfo     `json:"channels"`
	IceServers   []IceServerInfo   `json:"ice_servers"`
	Config       RoomConfigMsg     `json:"config"`
}

// ParticipantJoinedMsg notifies of a new participant.
type ParticipantJoinedMsg struct {
	RoomID      string          `json:"room_id"`
	Participant ParticipantInfo `json:"participant"`
}

// ParticipantLeftMsg notifies of a participant leaving.
type ParticipantLeftMsg struct {
	RoomID string `json:"room_id"`
	UserID string `json:"user_id"`
}

// MuteStateMsg carries mute state changes.
type MuteStateMsg struct {
	RoomID string `json:"room_id"`
	UserID string `json:"user_id"`
	Muted  bool   `json:"muted"`
}

// PTTStateMsg carries push-to-talk state.
type PTTStateMsg struct {
	RoomID  string `json:"room_id"`
	Pressed bool   `json:"pressed"`
}

// VADStateMsg carries voice activity detection state.
type VADStateMsg struct {
	RoomID   string `json:"room_id"`
	UserID   string `json:"user_id"`
	Speaking bool   `json:"speaking"`
}

// PositionUpdateMsg carries a position update from a client.
type PositionUpdateMsg struct {
	RoomID   string     `json:"room_id"`
	Position Position3D `json:"position"`
}

// SpatialParamEntry holds spatial audio parameters for one source.
type SpatialParamEntry struct {
	SourceUserID string  `json:"source_user_id"`
	Gain         float64 `json:"gain"`
	Azimuth      float64 `json:"azimuth"`
	Elevation    float64 `json:"elevation"`
	Distance     float64 `json:"distance"`
}

// SpatialParamsMsg carries computed spatial audio parameters for a listener.
type SpatialParamsMsg struct {
	RoomID string              `json:"room_id"`
	Params []SpatialParamEntry `json:"params"`
}

// ChannelJoinMsg is sent by clients to join a channel.
type ChannelJoinMsg struct {
	RoomID    string `json:"room_id"`
	ChannelID string `json:"channel_id"`
}

// ChannelLeaveMsg is sent by clients to leave a channel.
type ChannelLeaveMsg struct {
	RoomID    string `json:"room_id"`
	ChannelID string `json:"channel_id"`
}

// KickNotifyMsg notifies a user they have been kicked.
type KickNotifyMsg struct {
	RoomID          string `json:"room_id"`
	Reason          string `json:"reason"`
	CooldownSeconds int    `json:"cooldown_seconds"`
}

// BanNotifyMsg notifies a user they have been banned.
type BanNotifyMsg struct {
	RoomID          string `json:"room_id"`
	Reason          string `json:"reason"`
	DurationSeconds int64  `json:"duration_seconds"`
}

// ErrorMsg carries an error response to the client.
type ErrorMsg struct {
	Code    int               `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

// ModerationActionMsg is sent by moderators to perform moderation actions.
type ModerationActionMsg struct {
	RoomID          string `json:"room_id"`
	Action          string `json:"action"`
	TargetUserID    string `json:"target_user_id"`
	Reason          string `json:"reason"`
	DurationSeconds int64  `json:"duration_seconds"`
}

// WhisperMsg sets up a directed audio channel to a target user.
type WhisperMsg struct {
	RoomID       string `json:"room_id"`
	TargetUserID string `json:"target_user_id"`
}

// PingMsg is sent by clients for latency measurement.
type PingMsg struct {
	Timestamp int64 `json:"timestamp"`
}

// PongMsg is sent by the server in response to a ping.
type PongMsg struct {
	Timestamp  int64 `json:"timestamp"`
	ServerTime int64 `json:"server_time"`
}

// RecordingStateMsg notifies clients of recording state changes.
type RecordingStateMsg struct {
	RoomID      string `json:"room_id"`
	Recording   bool   `json:"recording"`
	RecordingID string `json:"recording_id,omitempty"`
}

// ChannelStateMsg notifies clients of channel membership changes.
type ChannelStateMsg struct {
	RoomID    string   `json:"room_id"`
	ChannelID string   `json:"channel_id"`
	Members   []string `json:"members"`
}

// SignalingEnvelope wraps all signaling messages with metadata.
type SignalingEnvelope struct {
	Opcode    int64       `json:"opcode"`
	Sequence  int64       `json:"sequence"`
	Timestamp int64       `json:"timestamp"`
	Payload   interface{} `json:"payload"`
}
