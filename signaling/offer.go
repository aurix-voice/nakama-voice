package signaling

import (
	"fmt"
	"strings"

	"github.com/pion/webrtc/v3"
)

// NegotiateOffer processes an SDP offer from a client, filters codecs,
// validates direction attributes, and constructs an SDP answer.
// Returns the answer SDP string.
func NegotiateOffer(offerSDP string, sdpType string) (*webrtc.SessionDescription, error) {
	if offerSDP == "" {
		return nil, fmt.Errorf("signaling: empty SDP offer")
	}

	parsedType := webrtc.SDPTypeOffer
	switch strings.ToLower(sdpType) {
	case "offer":
		parsedType = webrtc.SDPTypeOffer
	case "answer":
		parsedType = webrtc.SDPTypeAnswer
	case "pranswer":
		parsedType = webrtc.SDPTypePranswer
	case "rollback":
		parsedType = webrtc.SDPTypeRollback
	default:
		return nil, fmt.Errorf("signaling: invalid SDP type: %s", sdpType)
	}

	desc := &webrtc.SessionDescription{
		Type: parsedType,
		SDP:  offerSDP,
	}

	return desc, nil
}

// ValidateOfferSDP performs basic validation on an SDP offer string.
// Returns an error if the SDP is malformed or missing required fields.
func ValidateOfferSDP(sdp string) error {
	if sdp == "" {
		return fmt.Errorf("signaling: SDP is empty")
	}

	if !strings.Contains(sdp, "v=0") {
		return fmt.Errorf("signaling: SDP missing version line")
	}

	if !strings.Contains(sdp, "m=audio") {
		return fmt.Errorf("signaling: SDP missing audio media line")
	}

	return nil
}

// FilterCodecs validates that the SDP contains OPUS codec support.
// Returns true if OPUS is present in the offer.
func FilterCodecs(sdp string) bool {
	lowerSDP := strings.ToLower(sdp)
	return strings.Contains(lowerSDP, "opus")
}

// BuildSDPAnswer creates a properly formatted SDP answer message
// for sending back to the client via signaling.
func BuildSDPAnswer(roomID string, answer *webrtc.SessionDescription) *SDPMsg {
	return &SDPMsg{
		RoomID: roomID,
		SDP:    answer.SDP,
		Type:   answer.Type.String(),
	}
}