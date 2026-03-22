package signaling

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"

	"github.com/your-org/nakama-voice/metrics"
)

// Handler dispatches incoming signaling messages from Nakama's WebSocket
// to the appropriate handler function based on the opcode.
type Handler struct {
	// roomJoinHandler processes room join requests.
	roomJoinHandler func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID, sessionID string, msg *RoomJoinRequestMsg) error
	// sdpOfferHandler processes SDP offers from clients.
	sdpOfferHandler func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *SDPMsg) error
	// iceCandidateHandler processes ICE candidates from clients.
	iceCandidateHandler func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *ICECandidateMsg) error
	// roomLeaveHandler processes room leave requests.
	roomLeaveHandler func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *RoomLeaveMsg) error
	// muteStateHandler processes mute state changes.
	muteStateHandler func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *MuteStateMsg) error
	// pttStateHandler processes push-to-talk state changes.
	pttStateHandler func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *PTTStateMsg) error
	// positionHandler processes position updates.
	positionHandler func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *PositionUpdateMsg) error
	// channelJoinHandler processes channel join requests.
	channelJoinHandler func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *ChannelJoinMsg) error
	// channelLeaveHandler processes channel leave requests.
	channelLeaveHandler func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *ChannelLeaveMsg) error
	// moderationHandler processes moderation actions.
	moderationHandler func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *ModerationActionMsg) error
	// whisperHandler processes whisper setup.
	whisperHandler func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *WhisperMsg) error
	// pingHandler processes ping messages.
	pingHandler func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *PingMsg) error

	voiceMetrics *metrics.VoiceMetrics
}

// NewHandler creates a new signaling message handler with the given metrics.
func NewHandler(m *metrics.VoiceMetrics) *Handler {
	return &Handler{
		voiceMetrics: m,
	}
}

// SetRoomJoinHandler registers the handler for room join requests.
func (h *Handler) SetRoomJoinHandler(fn func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID, sessionID string, msg *RoomJoinRequestMsg) error) {
	h.roomJoinHandler = fn
}

// SetSDPOfferHandler registers the handler for SDP offers.
func (h *Handler) SetSDPOfferHandler(fn func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *SDPMsg) error) {
	h.sdpOfferHandler = fn
}

// SetICECandidateHandler registers the handler for ICE candidates.
func (h *Handler) SetICECandidateHandler(fn func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *ICECandidateMsg) error) {
	h.iceCandidateHandler = fn
}

// SetRoomLeaveHandler registers the handler for room leave requests.
func (h *Handler) SetRoomLeaveHandler(fn func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *RoomLeaveMsg) error) {
	h.roomLeaveHandler = fn
}

// SetMuteStateHandler registers the handler for mute state changes.
func (h *Handler) SetMuteStateHandler(fn func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *MuteStateMsg) error) {
	h.muteStateHandler = fn
}

// SetPTTStateHandler registers the handler for PTT state changes.
func (h *Handler) SetPTTStateHandler(fn func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *PTTStateMsg) error) {
	h.pttStateHandler = fn
}

// SetPositionHandler registers the handler for position updates.
func (h *Handler) SetPositionHandler(fn func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *PositionUpdateMsg) error) {
	h.positionHandler = fn
}

// SetChannelJoinHandler registers the handler for channel join requests.
func (h *Handler) SetChannelJoinHandler(fn func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *ChannelJoinMsg) error) {
	h.channelJoinHandler = fn
}

// SetChannelLeaveHandler registers the handler for channel leave requests.
func (h *Handler) SetChannelLeaveHandler(fn func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *ChannelLeaveMsg) error) {
	h.channelLeaveHandler = fn
}

// SetModerationHandler registers the handler for moderation actions.
func (h *Handler) SetModerationHandler(fn func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *ModerationActionMsg) error) {
	h.moderationHandler = fn
}

// SetWhisperHandler registers the handler for whisper setup.
func (h *Handler) SetWhisperHandler(fn func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *WhisperMsg) error) {
	h.whisperHandler = fn
}

// SetPingHandler registers the handler for ping messages.
func (h *Handler) SetPingHandler(fn func(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, msg *PingMsg) error) {
	h.pingHandler = fn
}

// HandleMessage is the main entry point for all incoming signaling messages.
// It decodes the envelope, dispatches to the appropriate handler, and returns
// a response payload or error.
func (h *Handler) HandleMessage(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID, sessionID string, opcode int64, data []byte) ([]byte, error) {
	opcodeStr := fmt.Sprintf("%d", opcode)

	if h.voiceMetrics != nil {
		h.voiceMetrics.IncSignalingMessage(opcodeStr)
	}

	// Parse the raw payload
	var payload json.RawMessage
	if len(data) > 0 {
		// Try to decode as envelope first
		_, envPayload, envErr := DecodeEnvelope(data)
		if envErr == nil && envPayload != nil {
			payload = envPayload
		} else {
			payload = json.RawMessage(data)
		}
	}

	var err error

	switch opcode {
	case OpRoomJoinRequest:
		msg, decErr := DecodeRoomJoinRequest(payload)
		if decErr != nil {
			return h.errorResponse(ErrInvalidPayload, decErr.Error(), opcode)
		}
		if h.roomJoinHandler != nil {
			err = h.roomJoinHandler(ctx, logger, nk, userID, sessionID, msg)
		}

	case OpSDPOffer:
		msg, decErr := DecodeSDPMsg(payload)
		if decErr != nil {
			return h.errorResponse(ErrInvalidSDP, decErr.Error(), opcode)
		}
		if h.sdpOfferHandler != nil {
			err = h.sdpOfferHandler(ctx, logger, nk, userID, msg)
		}

	case OpICECandidateLocal:
		msg, decErr := DecodeICECandidateMsg(payload)
		if decErr != nil {
			return h.errorResponse(ErrInvalidICE, decErr.Error(), opcode)
		}
		if h.iceCandidateHandler != nil {
			err = h.iceCandidateHandler(ctx, logger, nk, userID, msg)
		}

	case OpRoomLeave:
		msg, decErr := DecodeRoomLeaveMsg(payload)
		if decErr != nil {
			return h.errorResponse(ErrInvalidPayload, decErr.Error(), opcode)
		}
		if h.roomLeaveHandler != nil {
			err = h.roomLeaveHandler(ctx, logger, nk, userID, msg)
		}

	case OpMuteState:
		msg, decErr := DecodeMuteStateMsg(payload)
		if decErr != nil {
			return h.errorResponse(ErrInvalidPayload, decErr.Error(), opcode)
		}
		if h.muteStateHandler != nil {
			err = h.muteStateHandler(ctx, logger, nk, userID, msg)
		}

	case OpPTTState:
		msg, decErr := DecodePTTStateMsg(payload)
		if decErr != nil {
			return h.errorResponse(ErrInvalidPayload, decErr.Error(), opcode)
		}
		if h.pttStateHandler != nil {
			err = h.pttStateHandler(ctx, logger, nk, userID, msg)
		}

	case OpPositionUpdate:
		msg, decErr := DecodePositionUpdateMsg(payload)
		if decErr != nil {
			return h.errorResponse(ErrInvalidPayload, decErr.Error(), opcode)
		}
		if h.positionHandler != nil {
			err = h.positionHandler(ctx, logger, nk, userID, msg)
		}

	case OpChannelJoin:
		msg, decErr := DecodeChannelJoinMsg(payload)
		if decErr != nil {
			return h.errorResponse(ErrInvalidPayload, decErr.Error(), opcode)
		}
		if h.channelJoinHandler != nil {
			err = h.channelJoinHandler(ctx, logger, nk, userID, msg)
		}

	case OpChannelLeave:
		msg, decErr := DecodeChannelLeaveMsg(payload)
		if decErr != nil {
			return h.errorResponse(ErrInvalidPayload, decErr.Error(), opcode)
		}
		if h.channelLeaveHandler != nil {
			err = h.channelLeaveHandler(ctx, logger, nk, userID, msg)
		}

	case OpModerationAction:
		msg, decErr := DecodeModerationActionMsg(payload)
		if decErr != nil {
			return h.errorResponse(ErrInvalidPayload, decErr.Error(), opcode)
		}
		if h.moderationHandler != nil {
			err = h.moderationHandler(ctx, logger, nk, userID, msg)
		}

	case OpWhisper:
		msg, decErr := DecodeWhisperMsg(payload)
		if decErr != nil {
			return h.errorResponse(ErrInvalidPayload, decErr.Error(), opcode)
		}
		if h.whisperHandler != nil {
			err = h.whisperHandler(ctx, logger, nk, userID, msg)
		}

	case OpPing:
		msg, decErr := DecodePingMsg(payload)
		if decErr != nil {
			return h.errorResponse(ErrInvalidPayload, decErr.Error(), opcode)
		}
		if h.pingHandler != nil {
			err = h.pingHandler(ctx, logger, nk, userID, msg)
		}
		// Send pong
		return EncodeEnvelope(OpPong, 0, PongMsg{
			Timestamp:  msg.Timestamp,
			ServerTime: time.Now().UnixMilli(),
		})

	default:
		return h.errorResponse(ErrUnknown, fmt.Sprintf("unknown opcode: %d", opcode), opcode)
	}

	if err != nil {
		if h.voiceMetrics != nil {
			h.voiceMetrics.IncSignalingError(opcodeStr)
		}
		logger.Warn("signaling: handler error opcode=%d user=%s: %v", opcode, userID, err)
		return h.errorResponse(ErrInternal, err.Error(), opcode)
	}

	return nil, nil
}

func (h *Handler) errorResponse(code int, message string, opcode int64) ([]byte, error) {
	return EncodeError(code, message, map[string]string{"opcode": fmt.Sprintf("%d", opcode)}, 0)
}