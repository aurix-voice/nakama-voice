package sfu

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"

	"github.com/your-org/nakama-voice/metrics"
)

// PeerState represents the current state of an SFU peer connection.
type PeerState int

const (
	// PeerStateNew is the initial state before negotiation.
	PeerStateNew PeerState = iota
	// PeerStateConnecting indicates ICE/DTLS negotiation is in progress.
	PeerStateConnecting
	// PeerStateConnected indicates the peer connection is active.
	PeerStateConnected
	// PeerStateDisconnected indicates a temporary disconnection.
	PeerStateDisconnected
	// PeerStateFailed indicates the connection has failed.
	PeerStateFailed
	// PeerStateClosed indicates the peer connection is closed.
	PeerStateClosed
)

// Peer represents a single WebRTC peer connection for one participant.
// It manages ICE negotiation, DTLS-SRTP handshake, and track send/receive
// goroutines.
type Peer struct {
	userID    string
	sessionID string
	roomID    string
	pc        *webrtc.PeerConnection
	state     PeerState

	// onTrackReceived is called when a remote audio track is received.
	onTrackReceived func(userID string, track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver)
	// onICECandidate is called when a local ICE candidate is generated.
	onICECandidate func(userID string, candidate *webrtc.ICECandidate)
	// onStateChange is called when the peer connection state changes.
	onStateChange func(userID string, state PeerState)

	// sendTracks maps track ID to the local track used for sending audio to this peer.
	sendTracks map[string]*webrtc.TrackLocalStaticRTP

	// nackHandler manages NACK-based retransmission for this peer.
	nackHandler *NACKHandler
	// bwEstimator tracks available bandwidth for this peer.
	bwEstimator *BandwidthEstimator

	// cancel cancels all goroutines associated with this peer.
	cancel context.CancelFunc
	ctx    context.Context

	// metrics reference for counter updates.
	voiceMetrics *metrics.VoiceMetrics

	// mu protects state and sendTracks.
	mu sync.RWMutex
}

// PeerConfig holds configuration for creating a new Peer.
type PeerConfig struct {
	UserID       string
	SessionID    string
	RoomID       string
	ICEServers   []webrtc.ICEServer
	MaxBandwidth int
	DTLSMgr      *DTLSManager
	Metrics      *metrics.VoiceMetrics
}

// NewPeer creates a new SFU peer connection for the specified user.
// The peer is created in PeerStateNew and must be negotiated via
// SetRemoteDescription before audio can flow.
func NewPeer(parentCtx context.Context, cfg PeerConfig) (*Peer, error) {
	ctx, cancel := context.WithCancel(parentCtx)

	cert, err := cfg.DTLSMgr.GetCertificate()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("sfu: get dtls certificate: %w", err)
	}

	// Create MediaEngine with OPUS codec
	m := &webrtc.MediaEngine{}
	if regErr := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeOpus,
			ClockRate:   48000,
			Channels:    2,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); regErr != nil {
		cancel()
		return nil, fmt.Errorf("sfu: register opus codec: %w", regErr)
	}

	// Create interceptor registry
	i := &interceptor.Registry{}
	if useErr := webrtc.RegisterDefaultInterceptors(m, i); useErr != nil {
		cancel()
		return nil, fmt.Errorf("sfu: register interceptors: %w", useErr)
	}

	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(m),
		webrtc.WithInterceptorRegistry(i),
	)

	pcConfig := webrtc.Configuration{
		ICEServers:   cfg.ICEServers,
		Certificates: []webrtc.Certificate{*cert},
	}

	pc, pcErr := api.NewPeerConnection(pcConfig)
	if pcErr != nil {
		cancel()
		return nil, fmt.Errorf("sfu: create peer connection: %w", pcErr)
	}

	p := &Peer{
		userID:       cfg.UserID,
		sessionID:    cfg.SessionID,
		roomID:       cfg.RoomID,
		pc:           pc,
		state:        PeerStateNew,
		sendTracks:   make(map[string]*webrtc.TrackLocalStaticRTP, 8),
		nackHandler:  NewNACKHandler(0, 500, 2*time.Second),
		bwEstimator:  NewBandwidthEstimator(cfg.MaxBandwidth),
		cancel:       cancel,
		ctx:          ctx,
		voiceMetrics: cfg.Metrics,
	}

	// Set up connection state change handler
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		defer func() {
			if r := recover(); r != nil {
				// Recovered panic in connection state callback
			}
		}()

		var peerState PeerState
		switch state {
		case webrtc.PeerConnectionStateNew:
			peerState = PeerStateNew
		case webrtc.PeerConnectionStateConnecting:
			peerState = PeerStateConnecting
		case webrtc.PeerConnectionStateConnected:
			peerState = PeerStateConnected
		case webrtc.PeerConnectionStateDisconnected:
			peerState = PeerStateDisconnected
		case webrtc.PeerConnectionStateFailed:
			peerState = PeerStateFailed
		case webrtc.PeerConnectionStateClosed:
			peerState = PeerStateClosed
		}

		p.mu.Lock()
		p.state = peerState
		p.mu.Unlock()

		if cfg.Metrics != nil {
			cfg.Metrics.PeerConnectionStates.WithLabelValues(state.String()).Inc()
		}

		if p.onStateChange != nil {
			p.onStateChange(p.userID, peerState)
		}

		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			cancel()
		}
	})

	// Set up ICE candidate handler
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		defer func() {
			if r := recover(); r != nil {
				// Recovered panic in ICE candidate callback
			}
		}()
		if candidate != nil && p.onICECandidate != nil {
			p.onICECandidate(p.userID, candidate)
		}
	})

	// Set up track handler
	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		defer func() {
			if r := recover(); r != nil {
				// Recovered panic in track callback
			}
		}()
		if p.onTrackReceived != nil {
			p.onTrackReceived(p.userID, track, receiver)
		}
	})

	return p, nil
}

// SetRemoteDescription sets the remote SDP description (offer from client).
func (p *Peer) SetRemoteDescription(sdp webrtc.SessionDescription) error {
	if err := p.pc.SetRemoteDescription(sdp); err != nil {
		return fmt.Errorf("sfu: set remote description for %s: %w", p.userID, err)
	}
	return nil
}

// CreateAnswer creates an SDP answer after receiving an offer.
func (p *Peer) CreateAnswer() (*webrtc.SessionDescription, error) {
	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return nil, fmt.Errorf("sfu: create answer for %s: %w", p.userID, err)
	}
	if err := p.pc.SetLocalDescription(answer); err != nil {
		return nil, fmt.Errorf("sfu: set local description for %s: %w", p.userID, err)
	}
	return &answer, nil
}

// CreateOffer creates an SDP offer (for server-initiated renegotiation).
func (p *Peer) CreateOffer() (*webrtc.SessionDescription, error) {
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		return nil, fmt.Errorf("sfu: create offer for %s: %w", p.userID, err)
	}
	if err := p.pc.SetLocalDescription(offer); err != nil {
		return nil, fmt.Errorf("sfu: set local description for %s: %w", p.userID, err)
	}
	return &offer, nil
}

// AddICECandidate adds a remote ICE candidate to the peer connection.
func (p *Peer) AddICECandidate(candidate webrtc.ICECandidateInit) error {
	if err := p.pc.AddICECandidate(candidate); err != nil {
		return fmt.Errorf("sfu: add ice candidate for %s: %w", p.userID, err)
	}
	return nil
}

// AddSendTrack adds a local track to send audio to this peer.
// Returns the created track for writing RTP packets.
func (p *Peer) AddSendTrack(trackID, streamID string) (*webrtc.TrackLocalStaticRTP, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	track, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
		trackID,
		streamID,
	)
	if err != nil {
		return nil, fmt.Errorf("sfu: create local track for %s: %w", p.userID, err)
	}

	sender, err := p.pc.AddTrack(track)
	if err != nil {
		return nil, fmt.Errorf("sfu: add track to peer %s: %w", p.userID, err)
	}

	// Start RTCP reader goroutine for this sender
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// Recovered panic in RTCP reader goroutine
			}
		}()
		rtcpBuf := make([]byte, 1500)
		for {
			select {
			case <-p.ctx.Done():
				return
			default:
			}
			n, _, readErr := sender.Read(rtcpBuf)
			if readErr != nil {
				return
			}
			_ = n // RTCP packets processed by interceptors
		}
	}()

	p.sendTracks[trackID] = track
	return track, nil
}

// RemoveSendTrack removes a send track from this peer.
func (p *Peer) RemoveSendTrack(trackID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.sendTracks, trackID)
}

// WriteRTP writes an RTP packet to a send track.
func (p *Peer) WriteRTP(trackID string, pkt *rtp.Packet) error {
	p.mu.RLock()
	track, ok := p.sendTracks[trackID]
	p.mu.RUnlock()

	if !ok {
		return fmt.Errorf("sfu: track %s not found for peer %s", trackID, p.userID)
	}

	if err := track.WriteRTP(pkt); err != nil {
		return fmt.Errorf("sfu: write rtp to %s track %s: %w", p.userID, trackID, err)
	}
	return nil
}

// SetOnTrackReceived sets the callback for when a remote track is received.
func (p *Peer) SetOnTrackReceived(fn func(string, *webrtc.TrackRemote, *webrtc.RTPReceiver)) {
	p.onTrackReceived = fn
}

// SetOnICECandidate sets the callback for local ICE candidate generation.
func (p *Peer) SetOnICECandidate(fn func(string, *webrtc.ICECandidate)) {
	p.onICECandidate = fn
}

// SetOnStateChange sets the callback for peer connection state changes.
func (p *Peer) SetOnStateChange(fn func(string, PeerState)) {
	p.onStateChange = fn
}

// State returns the current peer connection state.
func (p *Peer) State() PeerState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.state
}

// UserID returns the user ID associated with this peer.
func (p *Peer) UserID() string {
	return p.userID
}

// SessionID returns the session ID associated with this peer.
func (p *Peer) SessionID() string {
	return p.sessionID
}

// Close closes the peer connection and cancels all goroutines.
func (p *Peer) Close() error {
	p.cancel()

	p.mu.Lock()
	p.state = PeerStateClosed
	p.mu.Unlock()

	if err := p.pc.Close(); err != nil {
		return fmt.Errorf("sfu: close peer %s: %w", p.userID, err)
	}
	return nil
}

// BandwidthEstimator returns the peer's bandwidth estimator.
func (p *Peer) BandwidthEstimator() *BandwidthEstimator {
	return p.bwEstimator
}

// NACKHandler returns the peer's NACK handler.
func (p *Peer) NACKHandler() *NACKHandler {
	return p.nackHandler
}