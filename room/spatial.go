package room

import (
	"sync"
	"time"

	"github.com/aurix-voice/nakama-voice/audio"
	"github.com/aurix-voice/nakama-voice/signaling"
)

// SpatialTracker manages per-room 3D position tracking with dead-reckoning
// interpolation and rate-limited update ingestion. It computes pairwise
// attenuation parameters for all participants.
type SpatialTracker struct {
	engine *audio.SpatialEngine

	// lastUpdateTime maps user IDs to their last position update time.
	lastUpdateTime map[string]time.Time
	// minUpdateInterval is the minimum time between position updates from
	// the same user to prevent flooding.
	minUpdateInterval time.Duration

	// mu protects lastUpdateTime.
	mu sync.RWMutex
}

// NewSpatialTracker creates a spatial tracker for a room with the specified
// attenuation model and maximum distance.
func NewSpatialTracker(model audio.AttenuationModel, maxDistance float64, hrtfEnabled bool) *SpatialTracker {
	return &SpatialTracker{
		engine:            audio.NewSpatialEngine(model, maxDistance, hrtfEnabled),
		lastUpdateTime:    make(map[string]time.Time, 32),
		minUpdateInterval: 50 * time.Millisecond,
	}
}

// UpdatePosition updates a participant's position. Returns false if the
// update was rate-limited.
func (st *SpatialTracker) UpdatePosition(userID string, pos signaling.Position3D) bool {
	st.mu.Lock()
	now := time.Now()
	if lastTime, ok := st.lastUpdateTime[userID]; ok {
		if now.Sub(lastTime) < st.minUpdateInterval {
			st.mu.Unlock()
			return false
		}
	}
	st.lastUpdateTime[userID] = now
	st.mu.Unlock()

	st.engine.UpdatePosition(userID, pos.X, pos.Y, pos.Z, pos.Yaw, pos.Pitch, now.UnixMilli())
	return true
}

// SetAudioCone configures the audio emission cone for a participant.
func (st *SpatialTracker) SetAudioCone(userID string, innerAngle, outerAngle, outerGain float64) {
	st.engine.SetAudioCone(userID, &audio.AudioCone{
		InnerAngle: innerAngle,
		OuterAngle: outerAngle,
		OuterGain:  outerGain,
	})
}

// RemoveUser removes a participant from spatial tracking.
func (st *SpatialTracker) RemoveUser(userID string) {
	st.mu.Lock()
	delete(st.lastUpdateTime, userID)
	st.mu.Unlock()
	st.engine.RemoveUser(userID)
}

// ComputeParamsForListener computes spatial audio parameters for all sources
// as heard by the specified listener.
func (st *SpatialTracker) ComputeParamsForListener(listenerID string) []signaling.SpatialParamEntry {
	now := time.Now().UnixMilli()
	params := st.engine.ComputeParams(listenerID, now)

	entries := make([]signaling.SpatialParamEntry, 0, len(params))
	for _, p := range params {
		entries = append(entries, signaling.SpatialParamEntry{
			SourceUserID: p.SourceUserID,
			Gain:         p.Gain,
			Azimuth:      p.Azimuth,
			Elevation:    p.Elevation,
			Distance:     p.Distance,
		})
	}
	return entries
}

// ComputeGain returns the spatial audio gain for a specific sender-receiver
// pair. Returns 1.0 if spatial audio is not active for either user.
func (st *SpatialTracker) ComputeGain(senderID, receiverID string) float64 {
	now := time.Now().UnixMilli()
	params := st.engine.ComputeParams(receiverID, now)
	for _, p := range params {
		if p.SourceUserID == senderID {
			return p.Gain
		}
	}
	return 1.0
}
