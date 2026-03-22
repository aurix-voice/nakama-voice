package signaling

import (
	"encoding/json"
	"fmt"
	"time"
)

// EncodeEnvelope wraps a payload in a SignalingEnvelope and marshals it to JSON.
// The sequence number should be maintained per-session by the caller.
func EncodeEnvelope(opcode int64, seq int64, payload interface{}) ([]byte, error) {
	env := SignalingEnvelope{
		Opcode:    opcode,
		Sequence:  seq,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
	data, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("signaling: encode envelope opcode=%d: %w", opcode, err)
	}
	return data, nil
}

// DecodeEnvelope parses a JSON byte slice into a SignalingEnvelope.
// The Payload field is left as json.RawMessage for further decoding
// based on the opcode.
func DecodeEnvelope(data []byte) (*SignalingEnvelope, json.RawMessage, error) {
	var raw struct {
		Opcode    int64           `json:"opcode"`
		Sequence  int64           `json:"sequence"`
		Timestamp int64           `json:"timestamp"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("signaling: decode envelope: %w", err)
	}
	env := &SignalingEnvelope{
		Opcode:    raw.Opcode,
		Sequence:  raw.Sequence,
		Timestamp: raw.Timestamp,
	}
	return env, raw.Payload, nil
}

// DecodeRoomJoinRequest decodes a RoomJoinRequestMsg from JSON.
func DecodeRoomJoinRequest(data json.RawMessage) (*RoomJoinRequestMsg, error) {
	var msg RoomJoinRequestMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("signaling: decode room join request: %w", err)
	}
	if msg.RoomID == "" {
		return nil, fmt.Errorf("signaling: room_id is required")
	}
	return &msg, nil
}

// DecodeSDPMsg decodes an SDPMsg from JSON.
func DecodeSDPMsg(data json.RawMessage) (*SDPMsg, error) {
	var msg SDPMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("signaling: decode sdp: %w", err)
	}
	if msg.RoomID == "" || msg.SDP == "" || msg.Type == "" {
		return nil, fmt.Errorf("signaling: sdp message requires room_id, sdp, and type")
	}
	return &msg, nil
}

// DecodeICECandidateMsg decodes an ICECandidateMsg from JSON.
func DecodeICECandidateMsg(data json.RawMessage) (*ICECandidateMsg, error) {
	var msg ICECandidateMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("signaling: decode ice candidate: %w", err)
	}
	if msg.RoomID == "" || msg.Candidate == "" {
		return nil, fmt.Errorf("signaling: ice candidate requires room_id and candidate")
	}
	return &msg, nil
}

// DecodeRoomLeaveMsg decodes a RoomLeaveMsg from JSON.
func DecodeRoomLeaveMsg(data json.RawMessage) (*RoomLeaveMsg, error) {
	var msg RoomLeaveMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("signaling: decode room leave: %w", err)
	}
	if msg.RoomID == "" {
		return nil, fmt.Errorf("signaling: room_id is required")
	}
	return &msg, nil
}

// DecodeMuteStateMsg decodes a MuteStateMsg from JSON.
func DecodeMuteStateMsg(data json.RawMessage) (*MuteStateMsg, error) {
	var msg MuteStateMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("signaling: decode mute state: %w", err)
	}
	if msg.RoomID == "" {
		return nil, fmt.Errorf("signaling: room_id is required")
	}
	return &msg, nil
}

// DecodePTTStateMsg decodes a PTTStateMsg from JSON.
func DecodePTTStateMsg(data json.RawMessage) (*PTTStateMsg, error) {
	var msg PTTStateMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("signaling: decode ptt state: %w", err)
	}
	if msg.RoomID == "" {
		return nil, fmt.Errorf("signaling: room_id is required")
	}
	return &msg, nil
}

// DecodePositionUpdateMsg decodes a PositionUpdateMsg from JSON.
func DecodePositionUpdateMsg(data json.RawMessage) (*PositionUpdateMsg, error) {
	var msg PositionUpdateMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("signaling: decode position update: %w", err)
	}
	if msg.RoomID == "" {
		return nil, fmt.Errorf("signaling: room_id is required")
	}
	return &msg, nil
}

// DecodeChannelJoinMsg decodes a ChannelJoinMsg from JSON.
func DecodeChannelJoinMsg(data json.RawMessage) (*ChannelJoinMsg, error) {
	var msg ChannelJoinMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("signaling: decode channel join: %w", err)
	}
	if msg.RoomID == "" || msg.ChannelID == "" {
		return nil, fmt.Errorf("signaling: room_id and channel_id are required")
	}
	return &msg, nil
}

// DecodeChannelLeaveMsg decodes a ChannelLeaveMsg from JSON.
func DecodeChannelLeaveMsg(data json.RawMessage) (*ChannelLeaveMsg, error) {
	var msg ChannelLeaveMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("signaling: decode channel leave: %w", err)
	}
	if msg.RoomID == "" || msg.ChannelID == "" {
		return nil, fmt.Errorf("signaling: room_id and channel_id are required")
	}
	return &msg, nil
}

// DecodeModerationActionMsg decodes a ModerationActionMsg from JSON.
func DecodeModerationActionMsg(data json.RawMessage) (*ModerationActionMsg, error) {
	var msg ModerationActionMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("signaling: decode moderation action: %w", err)
	}
	if msg.RoomID == "" || msg.Action == "" || msg.TargetUserID == "" {
		return nil, fmt.Errorf("signaling: room_id, action, and target_user_id are required")
	}
	return &msg, nil
}

// DecodeWhisperMsg decodes a WhisperMsg from JSON.
func DecodeWhisperMsg(data json.RawMessage) (*WhisperMsg, error) {
	var msg WhisperMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("signaling: decode whisper: %w", err)
	}
	if msg.RoomID == "" || msg.TargetUserID == "" {
		return nil, fmt.Errorf("signaling: room_id and target_user_id are required")
	}
	return &msg, nil
}

// DecodePingMsg decodes a PingMsg from JSON.
func DecodePingMsg(data json.RawMessage) (*PingMsg, error) {
	var msg PingMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("signaling: decode ping: %w", err)
	}
	return &msg, nil
}

// EncodeError creates a JSON-encoded error message.
func EncodeError(code int, message string, details map[string]string, seq int64) ([]byte, error) {
	return EncodeEnvelope(OpError, seq, ErrorMsg{
		Code:    code,
		Message: message,
		Details: details,
	})
}
