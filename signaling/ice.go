package signaling

import (
	"fmt"
	"strings"

	"github.com/pion/webrtc/v3"
)

// ParseICECandidate converts a signaling ICE candidate message into a
// WebRTC ICE candidate init structure for adding to a peer connection.
func ParseICECandidate(msg *ICECandidateMsg) (*webrtc.ICECandidateInit, error) {
	if msg.Candidate == "" {
		return nil, fmt.Errorf("signaling: empty ICE candidate")
	}

	sdpMid := msg.SDPMid
	sdpMLineIndex := uint16(msg.SDPMLineIndex)

	init := &webrtc.ICECandidateInit{
		Candidate:     msg.Candidate,
		SDPMid:        &sdpMid,
		SDPMLineIndex: &sdpMLineIndex,
	}

	return init, nil
}

// ValidateICECandidate performs basic validation on an ICE candidate string.
// Returns an error if the candidate appears malformed.
func ValidateICECandidate(candidate string) error {
	if candidate == "" {
		return fmt.Errorf("signaling: candidate is empty")
	}

	// ICE candidates should start with "candidate:" or "a=candidate:"
	trimmed := strings.TrimSpace(candidate)
	if !strings.HasPrefix(trimmed, "candidate:") && !strings.HasPrefix(trimmed, "a=candidate:") {
		return fmt.Errorf("signaling: invalid ICE candidate format")
	}

	return nil
}

// BuildICECandidateMsg creates a signaling message for an ICE candidate
// to send to a remote peer.
func BuildICECandidateMsg(roomID string, candidate *webrtc.ICECandidate) *ICECandidateMsg {
	init := candidate.ToJSON()

	sdpMid := ""
	if init.SDPMid != nil {
		sdpMid = *init.SDPMid
	}

	sdpMLineIndex := 0
	if init.SDPMLineIndex != nil {
		sdpMLineIndex = int(*init.SDPMLineIndex)
	}

	return &ICECandidateMsg{
		RoomID:        roomID,
		Candidate:     init.Candidate,
		SDPMid:        sdpMid,
		SDPMLineIndex: sdpMLineIndex,
	}
}
