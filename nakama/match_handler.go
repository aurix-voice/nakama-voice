package nakama

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"

	"github.com/your-org/nakama-voice/audio"
	"github.com/your-org/nakama-voice/config"
	"github.com/your-org/nakama-voice/metrics"
	"github.com/your-org/nakama-voice/room"
	"github.com/your-org/nakama-voice/sfu"
	"github.com/your-org/nakama-voice/signaling"
)

// VoiceMatchHandler implements all seven Nakama match lifecycle methods
// for voice rooms. MatchLoop runs at 10 Hz.
type VoiceMatchHandler struct{}

// VoiceMatchState holds the state for a voice match.
type VoiceMatchState struct {
	Room          *room.Room
	Engine        *sfu.Engine
	Config        *config.VoiceConfig
	Metrics       *metrics.VoiceMetrics
	Notifier      *Notifier
	Presence      *PresenceManager
	Dispatcher    runtime.MatchDispatcher
	TickCount     int64
	LastSpatialAt time.Time
}

// MatchInit initialises a new voice match. Called once when the match is created.
// Returns the initial state, tick rate, and match label.
func (h *VoiceMatchHandler) MatchInit(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, params map[string]interface{}) (interface{}, int, string) {
	roomID, _ := params["room_id"].(string)
	matchID, _ := params["match_id"].(string)
	label, _ := params["label"].(string)
	ownerID, _ := params["owner_id"].(string)

	if roomID == "" {
		roomID = matchID
	}
	if label == "" {
		label = "voice-" + roomID
	}

	// Retrieve shared subsystem references from params
	cfg, _ := params["config"].(*config.VoiceConfig)
	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	m, _ := params["metrics"].(*metrics.VoiceMetrics)
	engine, _ := params["engine"].(*sfu.Engine)

	roomCfg := cfg.GetRoom()
	audioCfg := cfg.GetAudio()
	voiceRoom := room.NewRoom(roomID, matchID, label, ownerID, roomCfg, audioCfg)

	if engine != nil {
		engine.CreateRoom(roomID)

		// Wire RTP callback: decode OPUS payload and feed PCM to VAD
		engine.SetOnRTPPacket(func(rtpRoomID, rtpUserID string, pkt *rtp.Packet) {
			dec := voiceRoom.GetDecoder(rtpUserID)
			if dec == nil {
				return
			}
			vad := voiceRoom.GetVAD(rtpUserID)
			if vad == nil {
				return
			}
			// Decode OPUS → mono PCM
			mono, decErr := dec.DecodeMono(pkt.Payload)
			if decErr != nil {
				return
			}
			// Downsample 48kHz → 16kHz for VAD
			pcm16k := audio.Downsample48to16(mono)
			if len(pcm16k) > 0 {
				vad.ProcessFrame(pcm16k)
			}
		})
	}

	state := &VoiceMatchState{
		Room:     voiceRoom,
		Engine:   engine,
		Config:   cfg,
		Metrics:  m,
		Notifier: NewNotifier(),
		Presence: NewPresenceManager(),
	}

	logger.Info("voice match: initialised room=%s match=%s owner=%s", roomID, matchID, ownerID)

	tickRate := 10 // 10 Hz = 100ms per tick
	return state, tickRate, label
}

// MatchJoinAttempt is called when a user tries to join the match.
// Returns whether the join is allowed.
func (h *VoiceMatchHandler) MatchJoinAttempt(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, presence runtime.Presence, metadata map[string]string) (interface{}, bool, string) {
	s := state.(*VoiceMatchState)

	// Check capacity
	if s.Room.ParticipantCount() >= s.Room.Config.MaxParticipants {
		return s, false, "room is full"
	}

	// Check ban
	userID := presence.GetUserId()
	banned, err := checkBan(ctx, nk, s.Room.ID, userID)
	if err != nil {
		logger.Warn("voice match: ban check error for %s: %v", userID, err)
	}
	if banned {
		return s, false, "user is banned"
	}

	logger.Info("voice match: join attempt approved user=%s room=%s", userID, s.Room.ID)
	return s, true, ""
}

// MatchJoin is called when a user successfully joins the match.
func (h *VoiceMatchHandler) MatchJoin(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, presences []runtime.Presence) interface{} {
	s := state.(*VoiceMatchState)
	joinStart := time.Now()

	// Store dispatcher reference for renegotiation callbacks
	if s.Dispatcher == nil {
		s.Dispatcher = dispatcher
		// Wire SDP renegotiation: when the SFU adds send tracks to existing
		// peers, it creates a new offer that must be delivered to the client.
		if s.Engine != nil {
			s.Engine.SetOnRenegotiationNeeded(func(roomID, userID string, offer *webrtc.SessionDescription) {
				offerMsg := signaling.SDPMsg{
					RoomID: roomID,
					SDP:    offer.SDP,
					Type:   offer.Type.String(),
				}
				offerData, encErr := signaling.EncodeEnvelope(signaling.OpSDPOffer, 0, offerMsg)
				if encErr != nil {
					return
				}
				target := &matchPresence{userID: userID}
				_ = dispatcher.BroadcastMessage(signaling.OpSDPOffer, offerData, []runtime.Presence{target}, nil, true)
			})
		}
	}

	for _, p := range presences {
		userID := p.GetUserId()
		sessionID := p.GetSessionId()
		username := p.GetUsername()

		if err := s.Room.AddParticipant(userID, sessionID, username); err != nil {
			logger.Warn("voice match: add participant failed user=%s: %v", userID, err)
			continue
		}

		// Join presence stream
		if err := s.Presence.JoinPresence(ctx, nk, s.Room.ID, userID, sessionID, username); err != nil {
			logger.Warn("voice match: join presence failed user=%s: %v", userID, err)
		}

		// Send room state to the joining user
		stateMsg := s.Room.RoomState(nil)
		stateData, err := json.Marshal(stateMsg)
		if err != nil {
			logger.Warn("voice match: marshal state: %v", err)
			continue
		}
		envData, err := signaling.EncodeEnvelope(signaling.OpRoomState, 0, json.RawMessage(stateData))
		if err != nil {
			logger.Warn("voice match: encode state envelope: %v", err)
			continue
		}
		_ = dispatcher.BroadcastMessageDeferred(signaling.OpRoomState, envData, []runtime.Presence{p}, p, true)

		// Notify other participants
		joinedMsg := signaling.ParticipantJoinedMsg{
			RoomID: s.Room.ID,
			Participant: signaling.ParticipantInfo{
				UserID:    userID,
				SessionID: sessionID,
				Username:  username,
				Role:      signaling.RoleUser,
				Channels:  []string{"default"},
			},
		}
		joinedData, _ := signaling.EncodeEnvelope(signaling.OpParticipantJoined, 0, joinedMsg)
		_ = dispatcher.BroadcastMessageDeferred(signaling.OpParticipantJoined, joinedData, nil, p, true)

		if s.Metrics != nil {
			s.Metrics.IncRoomJoined("all")
			s.Metrics.ObserveRoomJoinLatency(float64(time.Since(joinStart).Milliseconds()))
		}

		logger.Info("voice match: user joined user=%s room=%s", userID, s.Room.ID)
	}

	return s
}

// MatchLeave is called when a user leaves the match.
func (h *VoiceMatchHandler) MatchLeave(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, presences []runtime.Presence) interface{} {
	s := state.(*VoiceMatchState)

	for _, p := range presences {
		userID := p.GetUserId()
		sessionID := p.GetSessionId()

		s.Room.RemoveParticipant(userID)

		if s.Engine != nil {
			s.Engine.RemovePeer(s.Room.ID, userID)
		}

		// Leave presence stream
		_ = s.Presence.LeavePresence(ctx, nk, s.Room.ID, userID, sessionID)

		// Notify other participants
		leftMsg := signaling.ParticipantLeftMsg{
			RoomID: s.Room.ID,
			UserID: userID,
		}
		leftData, _ := signaling.EncodeEnvelope(signaling.OpParticipantLeft, 0, leftMsg)
		_ = dispatcher.BroadcastMessageDeferred(signaling.OpParticipantLeft, leftData, nil, p, true)

		if s.Metrics != nil {
			s.Metrics.DecRoomLeft("all")
		}

		logger.Info("voice match: user left user=%s room=%s", userID, s.Room.ID)
	}

	return s
}

// MatchLoop is called at the configured tick rate (10 Hz).
// Handles message processing, VAD broadcast, spatial updates, and housekeeping.
func (h *VoiceMatchHandler) MatchLoop(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, messages []runtime.MatchData) interface{} {
	s := state.(*VoiceMatchState)
	s.TickCount++

	// Process incoming messages
	for _, msg := range messages {
		if processErr := h.processMessage(ctx, logger, nk, dispatcher, s, msg); processErr != nil {
			logger.Warn("voice match: message processing error opcode=%d user=%s: %v",
				msg.GetOpCode(), msg.GetUserId(), processErr)
		}
	}

	// Broadcast VAD state every 5 ticks (500ms)
	if s.TickCount%5 == 0 {
		h.broadcastVADState(s, dispatcher)
	}

	// Update spatial audio every 3 ticks (300ms)
	if s.Room.Spatial() != nil && s.TickCount%3 == 0 {
		h.broadcastSpatialParams(s, dispatcher)
	}

	// GC disconnected peers every 100 ticks (10s)
	if s.TickCount%100 == 0 {
		h.cleanupDisconnectedPeers(s, logger)
	}

	// Terminate match if empty for over 60 seconds
	if s.Room.IsEmpty() && s.TickCount > 600 {
		return nil // Returning nil terminates the match
	}

	return s
}

// MatchTerminate is called when the match is being terminated.
func (h *VoiceMatchHandler) MatchTerminate(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, graceSeconds int) interface{} {
	s := state.(*VoiceMatchState)

	if s.Engine != nil {
		s.Engine.DestroyRoom(s.Room.ID)
	}

	logger.Info("voice match: terminated room=%s", s.Room.ID)
	return s
}

// MatchSignal handles external signals sent to the match.
func (h *VoiceMatchHandler) MatchSignal(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, data string) (interface{}, string) {
	s := state.(*VoiceMatchState)
	logger.Debug("voice match: signal received room=%s data=%s", s.Room.ID, data)
	return s, "ok"
}

func (h *VoiceMatchHandler) processMessage(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, s *VoiceMatchState, msg runtime.MatchData) error {
	userID := msg.GetUserId()
	opcode := msg.GetOpCode()
	data := msg.GetData()

	switch opcode {
	case signaling.OpSDPOffer:
		var sdpMsg signaling.SDPMsg
		if err := json.Unmarshal(data, &sdpMsg); err != nil {
			return fmt.Errorf("parse sdp offer: %w", err)
		}
		if s.Engine == nil {
			return fmt.Errorf("sfu engine not available")
		}
		peer := s.Engine.GetPeer(userID)
		if peer == nil {
			// Create peer on first SDP offer
			var createErr error
			peer, createErr = s.Engine.AddPeer(s.Room.ID, userID, "")
			if createErr != nil {
				return fmt.Errorf("create peer: %w", createErr)
			}
		}
		desc, descErr := signaling.NegotiateOffer(sdpMsg.SDP, sdpMsg.Type)
		if descErr != nil {
			return fmt.Errorf("negotiate offer: %w", descErr)
		}
		if setErr := peer.SetRemoteDescription(*desc); setErr != nil {
			return fmt.Errorf("set remote description: %w", setErr)
		}
		answer, ansErr := peer.CreateAnswer()
		if ansErr != nil {
			return fmt.Errorf("create answer: %w", ansErr)
		}
		answerMsg := signaling.BuildSDPAnswer(s.Room.ID, answer)
		answerData, encErr := signaling.EncodeEnvelope(signaling.OpSDPAnswer, 0, answerMsg)
		if encErr != nil {
			return fmt.Errorf("encode answer: %w", encErr)
		}
		senderPresence := &matchPresence{userID: userID}
		_ = dispatcher.BroadcastMessage(signaling.OpSDPAnswer, answerData, []runtime.Presence{senderPresence}, nil, true)

	case signaling.OpICECandidateLocal:
		var iceMsg signaling.ICECandidateMsg
		if err := json.Unmarshal(data, &iceMsg); err != nil {
			return fmt.Errorf("parse ice candidate: %w", err)
		}
		if s.Engine == nil {
			return fmt.Errorf("sfu engine not available")
		}
		peer := s.Engine.GetPeer(userID)
		if peer == nil {
			return fmt.Errorf("no peer for user %s", userID)
		}
		candidate, parseErr := signaling.ParseICECandidate(&iceMsg)
		if parseErr != nil {
			return fmt.Errorf("parse ice: %w", parseErr)
		}
		if addErr := peer.AddICECandidate(*candidate); addErr != nil {
			return fmt.Errorf("add ice candidate: %w", addErr)
		}

	case signaling.OpMuteState:
		var muteMsg signaling.MuteStateMsg
		if err := json.Unmarshal(data, &muteMsg); err != nil {
			return fmt.Errorf("parse mute state: %w", err)
		}
		s.Room.SetMuted(muteMsg.UserID, muteMsg.Muted)
		_ = dispatcher.BroadcastMessage(signaling.OpMuteState, data, nil, nil, true)

	case signaling.OpPTTState:
		var pttMsg signaling.PTTStateMsg
		if err := json.Unmarshal(data, &pttMsg); err != nil {
			return fmt.Errorf("parse ptt state: %w", err)
		}
		ch := s.Room.Channels().GetChannel("default")
		if ch != nil {
			ch.SetPTTState(userID, pttMsg.Pressed)
		}

	case signaling.OpPositionUpdate:
		var posMsg signaling.PositionUpdateMsg
		if err := json.Unmarshal(data, &posMsg); err != nil {
			return fmt.Errorf("parse position: %w", err)
		}
		if s.Room.Spatial() != nil {
			s.Room.Spatial().UpdatePosition(userID, posMsg.Position)
		}

	case signaling.OpChannelJoin:
		var chMsg signaling.ChannelJoinMsg
		if err := json.Unmarshal(data, &chMsg); err != nil {
			return fmt.Errorf("parse channel join: %w", err)
		}
		_ = s.Room.Channels().JoinUserToChannel(userID, chMsg.ChannelID)

	case signaling.OpChannelLeave:
		var chMsg signaling.ChannelLeaveMsg
		if err := json.Unmarshal(data, &chMsg); err != nil {
			return fmt.Errorf("parse channel leave: %w", err)
		}
		_ = s.Room.Channels().LeaveUserFromChannel(userID, chMsg.ChannelID)

	default:
		logger.Debug("voice match: unhandled opcode=%d user=%s", opcode, userID)
	}

	return nil
}

func (h *VoiceMatchHandler) broadcastVADState(s *VoiceMatchState, dispatcher runtime.MatchDispatcher) {
	for _, uid := range s.Room.ParticipantIDs() {
		vad := s.Room.GetVAD(uid)
		if vad == nil {
			continue
		}
		speaking := vad.IsSpeaking()
		s.Room.SetSpeaking(uid, speaking)

		vadMsg := signaling.VADStateMsg{
			RoomID:   s.Room.ID,
			UserID:   uid,
			Speaking: speaking,
		}
		data, err := json.Marshal(vadMsg)
		if err != nil {
			continue
		}
		_ = dispatcher.BroadcastMessage(signaling.OpVADState, data, nil, nil, true)
	}
}

func (h *VoiceMatchHandler) broadcastSpatialParams(s *VoiceMatchState, dispatcher runtime.MatchDispatcher) {
	spatial := s.Room.Spatial()
	if spatial == nil {
		return
	}

	for _, listenerID := range s.Room.ParticipantIDs() {
		params := spatial.ComputeParamsForListener(listenerID)
		if len(params) == 0 {
			continue
		}

		spatialMsg := signaling.SpatialParamsMsg{
			RoomID: s.Room.ID,
			Params: params,
		}
		data, err := json.Marshal(spatialMsg)
		if err != nil {
			continue
		}

		// Send only to this listener
		p := s.Room.GetParticipant(listenerID)
		if p == nil {
			continue
		}
		_ = dispatcher.BroadcastMessage(signaling.OpSpatialParams, data, []runtime.Presence{
			&matchPresence{userID: p.UserID, sessionID: p.SessionID, username: p.Username},
		}, nil, true)
	}
}

func (h *VoiceMatchHandler) cleanupDisconnectedPeers(s *VoiceMatchState, logger runtime.Logger) {
	if s.Engine == nil {
		return
	}
	for _, uid := range s.Room.ParticipantIDs() {
		peer := s.Engine.GetPeer(uid)
		if peer != nil && peer.State() == sfu.PeerStateFailed {
			s.Room.RemoveParticipant(uid)
			s.Engine.RemovePeer(s.Room.ID, uid)
			logger.Info("voice match: cleaned up disconnected peer user=%s room=%s", uid, s.Room.ID)
		}
	}
}

func checkBan(ctx context.Context, nk runtime.NakamaModule, roomID, userID string) (bool, error) {
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{Collection: "voice_bans", Key: fmt.Sprintf("%s:%s", roomID, userID), UserID: ""},
	})
	if err != nil {
		return false, err
	}
	return len(objs) > 0, nil
}

// matchPresence implements runtime.Presence for targeted broadcasts.
type matchPresence struct {
	userID    string
	sessionID string
	username  string
	node      string
}

func (p *matchPresence) GetUserId() string    { return p.userID }
func (p *matchPresence) GetSessionId() string  { return p.sessionID }
func (p *matchPresence) GetUsername() string    { return p.username }
func (p *matchPresence) GetNodeId() string     { return p.node }
func (p *matchPresence) GetHidden() bool       { return false }
func (p *matchPresence) GetPersistence() bool  { return false }
func (p *matchPresence) GetStatus() string     { return "" }
func (p *matchPresence) GetReason() runtime.PresenceReason { return runtime.PresenceReasonUnknown }