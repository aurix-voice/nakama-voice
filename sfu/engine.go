package sfu

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"

	"github.com/your-org/nakama-voice/config"
	"github.com/your-org/nakama-voice/metrics"
)

// Engine is the core SFU engine that manages the peer connection pool,
// orchestrates track routing, and owns the lifecycle of all peer goroutines.
type Engine struct {
	cfg     *config.VoiceConfig
	dtlsMgr *DTLSManager

	// rooms maps room ID to the RTP router for that room.
	rooms map[string]*Router

	// peers maps user ID to their SFU peer connection.
	peers map[string]*Peer

	// simulcastMgrs maps room ID to simulcast manager.
	simulcastMgrs map[string]*SimulcastManager

	// onRTPPacket is called for every received RTP packet (e.g., for VAD).
	onRTPPacket func(roomID, userID string, pkt *rtp.Packet)

	// onRenegotiationNeeded is called when a peer needs SDP renegotiation
	// (e.g., after new send tracks are added). The callback receives the
	// userID and a new SDP offer that must be delivered to the client.
	onRenegotiationNeeded func(roomID, userID string, offer *webrtc.SessionDescription)

	voiceMetrics *metrics.VoiceMetrics

	ctx    context.Context
	cancel context.CancelFunc

	// mu protects rooms, peers, simulcastMgrs.
	mu sync.RWMutex
}

// NewEngine creates a new SFU engine with the given configuration and metrics.
func NewEngine(parentCtx context.Context, cfg *config.VoiceConfig, m *metrics.VoiceMetrics) (*Engine, error) {
	ctx, cancel := context.WithCancel(parentCtx)

	dtlsMgr, err := NewDTLSManager(24 * time.Hour)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("sfu: create dtls manager: %w", err)
	}

	e := &Engine{
		cfg:           cfg,
		dtlsMgr:       dtlsMgr,
		rooms:         make(map[string]*Router, 64),
		peers:         make(map[string]*Peer, 256),
		simulcastMgrs: make(map[string]*SimulcastManager, 64),
		voiceMetrics:  m,
		ctx:           ctx,
		cancel:        cancel,
	}

	// Start background cleanup goroutine
	go e.cleanupLoop()

	return e, nil
}

// CreateRoom creates an RTP router for a new room.
func (e *Engine) CreateRoom(roomID string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if _, exists := e.rooms[roomID]; !exists {
		e.rooms[roomID] = NewRouter(roomID)
		sfuCfg := e.cfg.GetSFU()
		if sfuCfg.SimulcastEnabled {
			e.simulcastMgrs[roomID] = NewSimulcastManager()
		}
	}
}

// DestroyRoom removes a room's router and disconnects all peers in it.
func (e *Engine) DestroyRoom(roomID string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	delete(e.rooms, roomID)
	delete(e.simulcastMgrs, roomID)

	// Close all peers in this room
	for uid, peer := range e.peers {
		if peer.roomID == roomID {
			_ = peer.Close()
			delete(e.peers, uid)
		}
	}
}

// AddPeer creates a new WebRTC peer connection for a user in a room.
// Returns the Peer for further signaling operations.
func (e *Engine) AddPeer(roomID, userID, sessionID string) (*Peer, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	router, ok := e.rooms[roomID]
	if !ok {
		return nil, fmt.Errorf("sfu: room %s not found", roomID)
	}

	sfuCfg := e.cfg.GetSFU()

	// Build ICE server list
	iceServers := make([]webrtc.ICEServer, 0, len(sfuCfg.STUNServers)+len(sfuCfg.TURNServers))
	for _, url := range sfuCfg.STUNServers {
		iceServers = append(iceServers, webrtc.ICEServer{URLs: []string{url}})
	}
	for _, ts := range sfuCfg.TURNServers {
		iceServers = append(iceServers, webrtc.ICEServer{
			URLs:       []string{ts.URL},
			Username:   ts.Username,
			Credential: ts.Credential,
		})
	}

	peerCfg := PeerConfig{
		UserID:       userID,
		SessionID:    sessionID,
		RoomID:       roomID,
		ICEServers:   iceServers,
		MaxBandwidth: sfuCfg.MaxBandwidthKbps,
		DTLSMgr:      e.dtlsMgr,
		Metrics:      e.voiceMetrics,
	}

	peer, err := NewPeer(e.ctx, peerCfg)
	if err != nil {
		return nil, fmt.Errorf("sfu: create peer for %s in %s: %w", userID, roomID, err)
	}

	// Set up track received handler
	peer.SetOnTrackReceived(func(uid string, track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		e.handleRemoteTrack(roomID, uid, track, receiver)
	})

	// Set up state change handler
	peer.SetOnStateChange(func(uid string, state PeerState) {
		if state == PeerStateFailed || state == PeerStateClosed {
			e.RemovePeer(roomID, uid)
		}
	})

	// Add receiver queue in router
	rq := router.AddReceiver(userID, generateSSRC(userID), 256)
	router.AddSender(userID, 0) // SSRC set when track arrives

	e.peers[userID] = peer

	// Start a writer goroutine that drains the receiver queue and forwards
	// RTP packets to this peer's send tracks. This is the critical forwarding
	// path: Router.RoutePacket() puts TaggedPackets into ReceiverQueue.Packets,
	// and this goroutine reads them and calls Peer.WriteRTP() using the
	// sender's track ID to route to the correct send track.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// Recovered panic in receiver queue writer goroutine
			}
		}()
		for tagged := range rq.Packets {
			p := e.GetPeer(userID)
			if p == nil {
				return
			}
			trackID := "audio-" + tagged.SenderID
			if writeErr := p.WriteRTP(trackID, tagged.Packet); writeErr != nil {
				// Track may not exist yet; ignore until send track is added
				continue
			}
		}
	}()

	// Add send tracks on existing peers for this new user's audio, and
	// add send tracks on this new peer for existing users' audio.
	// This enables SDP renegotiation so audio actually flows.
	for existingUID, existingPeer := range e.peers {
		if existingUID == userID {
			continue
		}
		// Add a send track on the existing peer to carry the NEW user's audio to them.
		// Track ID = "audio-<newUserID>" so the writer goroutine for existingPeer can
		// match packets from the new user's receiver queue.
		trackIDForExisting := "audio-" + userID
		_, _ = existingPeer.AddSendTrack(trackIDForExisting, "voice-"+roomID)

		// Add a send track on the new peer to carry the EXISTING user's audio to them.
		trackIDForNew := "audio-" + existingUID
		_, _ = peer.AddSendTrack(trackIDForNew, "voice-"+roomID)

		// Trigger SDP renegotiation for the existing peer so the client
		// learns about the new send track.
		if e.onRenegotiationNeeded != nil {
			offer, offerErr := existingPeer.CreateOffer()
			if offerErr == nil && offer != nil {
				e.onRenegotiationNeeded(roomID, existingUID, offer)
			}
		}
	}

	return peer, nil
}

// RemovePeer removes a peer from a room and cleans up resources.
func (e *Engine) RemovePeer(roomID, userID string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if peer, ok := e.peers[userID]; ok {
		_ = peer.Close()
		delete(e.peers, userID)
	}

	if router, ok := e.rooms[roomID]; ok {
		router.RemoveSender(userID)
		router.RemoveReceiver(userID)
	}

	if sm, ok := e.simulcastMgrs[roomID]; ok {
		sm.UnregisterSender(userID)
		sm.RemoveReceiver(userID)
	}
}

// GetPeer returns the peer for a user, or nil if not found.
func (e *Engine) GetPeer(userID string) *Peer {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.peers[userID]
}

// GetRouter returns the router for a room, or nil if not found.
func (e *Engine) GetRouter(roomID string) *Router {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.rooms[roomID]
}

// SetOnRTPPacket sets a callback invoked for every received RTP packet.
func (e *Engine) SetOnRTPPacket(fn func(roomID, userID string, pkt *rtp.Packet)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onRTPPacket = fn
}

// SetOnRenegotiationNeeded sets a callback invoked when a peer needs SDP
// renegotiation (new tracks added). The match handler should deliver the
// offer to the client via the match dispatcher.
func (e *Engine) SetOnRenegotiationNeeded(fn func(roomID, userID string, offer *webrtc.SessionDescription)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onRenegotiationNeeded = fn
}

// SetForwardingEligibility sets the forwarding eligibility function for a room.
func (e *Engine) SetForwardingEligibility(roomID string, fn ForwardingEligibilityFunc) {
	e.mu.RLock()
	router, ok := e.rooms[roomID]
	e.mu.RUnlock()
	if ok {
		router.SetEligibilityFunc(fn)
	}
}

// handleRemoteTrack is called when a remote audio track is received from a peer.
// It starts a goroutine that reads RTP packets and routes them.
func (e *Engine) handleRemoteTrack(roomID, userID string, track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// Recovered panic in remote track reader goroutine
			}
		}()

		buf := make([]byte, 1500)
		for {
			n, _, readErr := track.Read(buf)
			if readErr != nil {
				return
			}

			pkt := &rtp.Packet{}
			if parseErr := pkt.Unmarshal(buf[:n]); parseErr != nil {
				continue
			}

			// Invoke RTP callback (for VAD, recording, etc.)
			e.mu.RLock()
			rtpCallback := e.onRTPPacket
			router := e.rooms[roomID]
			e.mu.RUnlock()

			if rtpCallback != nil {
				rtpCallback(roomID, userID, pkt)
			}

			// Route to other peers
			if router != nil {
				forwarded := router.RoutePacket(userID, pkt)
				if e.voiceMetrics != nil && forwarded > 0 {
					e.voiceMetrics.IncRTPForwarded(roomID, len(pkt.Payload))
				}
			}
		}
	}()
}

// PeerCount returns the total number of active peers.
func (e *Engine) PeerCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.peers)
}

// RoomCount returns the total number of active rooms.
func (e *Engine) RoomCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.rooms)
}

// Close shuts down the SFU engine and all peer connections.
func (e *Engine) Close() error {
	e.cancel()

	e.mu.Lock()
	defer e.mu.Unlock()

	for uid, peer := range e.peers {
		_ = peer.Close()
		delete(e.peers, uid)
	}

	return nil
}

// cleanupLoop runs background maintenance tasks.
func (e *Engine) cleanupLoop() {
	defer func() {
		if r := recover(); r != nil {
			// Recovered panic in cleanup loop
		}
	}()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.mu.RLock()
			for _, peer := range e.peers {
				peer.nackHandler.CleanupExpired()
			}
			e.mu.RUnlock()
		}
	}
}

// generateSSRC creates a deterministic SSRC from a user ID.
func generateSSRC(userID string) uint32 {
	var hash uint32
	for _, c := range userID {
		hash = hash*31 + uint32(c)
	}
	if hash == 0 {
		hash = 1
	}
	return hash
}