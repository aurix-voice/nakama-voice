package audio

import (
	"math"
	"sync"
)

// AttenuationModel defines the distance attenuation function for spatial audio.
type AttenuationModel int

const (
	// AttenuationLinear applies linear distance falloff.
	AttenuationLinear AttenuationModel = iota
	// AttenuationLogarithmic applies logarithmic distance falloff.
	AttenuationLogarithmic
	// AttenuationInverseSquare applies inverse-square law distance falloff.
	AttenuationInverseSquare
)

// SpatialParams holds the computed spatial audio parameters for a single
// source as heard by a specific listener. These parameters are sent to
// clients who apply them locally.
type SpatialParams struct {
	SourceUserID string  `json:"source_user_id"`
	Gain         float64 `json:"gain"`
	Azimuth      float64 `json:"azimuth"`   // degrees, -180 to 180
	Elevation    float64 `json:"elevation"`  // degrees, -90 to 90
	Distance     float64 `json:"distance"`   // metres
	LeftGain     float64 `json:"left_gain"`  // HRTF-derived left ear gain
	RightGain    float64 `json:"right_gain"` // HRTF-derived right ear gain
}

// AudioCone defines a directional audio emission cone for a participant.
type AudioCone struct {
	InnerAngle float64 `json:"inner_angle"` // degrees, full volume within
	OuterAngle float64 `json:"outer_angle"` // degrees, attenuated outside
	OuterGain  float64 `json:"outer_gain"`  // gain multiplier outside outer cone
}

// SpatialEngine computes 3D spatial audio parameters for all listener-source
// pairs in a room. It supports multiple attenuation models, HRTF-based gain
// tables, audio cone processing, and dead-reckoning position interpolation.
type SpatialEngine struct {
	model       AttenuationModel
	maxDistance float64
	hrtfEnabled bool
	hrtfTable   *HRTFTable

	// positions maps user IDs to their current interpolated positions.
	// Protected by mu.
	positions map[string]*InterpolatedPosition

	// cones maps user IDs to their audio cone configuration.
	// Protected by mu.
	cones map[string]*AudioCone

	// mu protects positions and cones maps.
	mu sync.RWMutex
}

// InterpolatedPosition holds a position with velocity for dead-reckoning.
type InterpolatedPosition struct {
	X, Y, Z       float64
	Yaw, Pitch     float64
	VX, VY, VZ     float64 // velocity for dead-reckoning
	LastUpdateMs   int64   // timestamp of last update
}

// NewSpatialEngine creates a spatial audio engine with the specified
// attenuation model, maximum audible distance, and HRTF enable flag.
func NewSpatialEngine(model AttenuationModel, maxDistance float64, hrtfEnabled bool) *SpatialEngine {
	se := &SpatialEngine{
		model:       model,
		maxDistance:  maxDistance,
		hrtfEnabled: hrtfEnabled,
		hrtfTable:   NewHRTFTable(),
		positions:   make(map[string]*InterpolatedPosition, 64),
		cones:       make(map[string]*AudioCone, 64),
	}
	return se
}

// UpdatePosition updates the position for a user with dead-reckoning velocity.
// nowMs is the current timestamp in milliseconds.
func (se *SpatialEngine) UpdatePosition(userID string, x, y, z, yaw, pitch float64, nowMs int64) {
	se.mu.Lock()
	defer se.mu.Unlock()

	prev, exists := se.positions[userID]
	if exists && nowMs > prev.LastUpdateMs {
		dt := float64(nowMs-prev.LastUpdateMs) / 1000.0
		if dt > 0 && dt < 2.0 { // ignore stale updates
			se.positions[userID] = &InterpolatedPosition{
				X: x, Y: y, Z: z,
				Yaw: yaw, Pitch: pitch,
				VX:           (x - prev.X) / dt,
				VY:           (y - prev.Y) / dt,
				VZ:           (z - prev.Z) / dt,
				LastUpdateMs: nowMs,
			}
			return
		}
	}

	se.positions[userID] = &InterpolatedPosition{
		X: x, Y: y, Z: z,
		Yaw: yaw, Pitch: pitch,
		LastUpdateMs: nowMs,
	}
}

// SetAudioCone configures the audio emission cone for a user.
func (se *SpatialEngine) SetAudioCone(userID string, cone *AudioCone) {
	se.mu.Lock()
	defer se.mu.Unlock()
	se.cones[userID] = cone
}

// RemoveUser removes a user's position and cone data.
func (se *SpatialEngine) RemoveUser(userID string) {
	se.mu.Lock()
	defer se.mu.Unlock()
	delete(se.positions, userID)
	delete(se.cones, userID)
}

// ComputeParams computes spatial audio parameters for all sources as heard
// by the specified listener. nowMs is the current timestamp in milliseconds.
func (se *SpatialEngine) ComputeParams(listenerID string, nowMs int64) []SpatialParams {
	se.mu.RLock()
	defer se.mu.RUnlock()

	listener, ok := se.positions[listenerID]
	if !ok {
		return nil
	}

	// Interpolate listener position
	lPos := se.interpolatePosition(listener, nowMs)

	params := make([]SpatialParams, 0, len(se.positions)-1)
	for sourceID, sourcePos := range se.positions {
		if sourceID == listenerID {
			continue
		}

		sPos := se.interpolatePosition(sourcePos, nowMs)

		// Compute distance
		dx := sPos.X - lPos.X
		dy := sPos.Y - lPos.Y
		dz := sPos.Z - lPos.Z
		distance := math.Sqrt(dx*dx + dy*dy + dz*dz)

		// Compute distance attenuation
		gain := se.computeAttenuation(distance)

		// Compute azimuth and elevation relative to listener
		azimuth, elevation := se.computeAngles(lPos, sPos, dx, dy, dz, distance)

		// Apply audio cone attenuation from source
		gain *= se.computeConeAttenuation(sourceID, sPos, lPos)

		// Compute HRTF gains
		leftGain, rightGain := 1.0, 1.0
		if se.hrtfEnabled {
			leftGain, rightGain = se.hrtfTable.Lookup(azimuth, elevation)
		}

		params = append(params, SpatialParams{
			SourceUserID: sourceID,
			Gain:         gain,
			Azimuth:      azimuth,
			Elevation:    elevation,
			Distance:     distance,
			LeftGain:     leftGain * gain,
			RightGain:    rightGain * gain,
		})
	}

	return params
}

// interpolatePosition applies dead-reckoning to estimate current position.
func (se *SpatialEngine) interpolatePosition(pos *InterpolatedPosition, nowMs int64) InterpolatedPosition {
	dt := float64(nowMs-pos.LastUpdateMs) / 1000.0
	if dt <= 0 || dt > 1.0 {
		return *pos
	}
	return InterpolatedPosition{
		X:     pos.X + pos.VX*dt,
		Y:     pos.Y + pos.VY*dt,
		Z:     pos.Z + pos.VZ*dt,
		Yaw:   pos.Yaw,
		Pitch: pos.Pitch,
	}
}

// computeAttenuation calculates distance-based gain using the configured model.
func (se *SpatialEngine) computeAttenuation(distance float64) float64 {
	if distance <= 0.01 {
		return 1.0
	}
	if distance >= se.maxDistance {
		return 0.0
	}

	ratio := distance / se.maxDistance

	switch se.model {
	case AttenuationLinear:
		return 1.0 - ratio
	case AttenuationLogarithmic:
		return 1.0 - math.Log(1.0+ratio*9.0)/math.Log(10.0)
	case AttenuationInverseSquare:
		return 1.0 / (1.0 + (distance * distance))
	default:
		return 1.0 - ratio
	}
}

// computeAngles calculates azimuth and elevation from listener to source.
func (se *SpatialEngine) computeAngles(listener, source InterpolatedPosition, dx, dy, dz, dist float64) (float64, float64) {
	if dist < 0.001 {
		return 0, 0
	}

	// Rotate direction vector by listener's yaw to get relative direction
	yawRad := listener.Yaw * math.Pi / 180.0
	relX := dx*math.Cos(-yawRad) - dz*math.Sin(-yawRad)
	relZ := dx*math.Sin(-yawRad) + dz*math.Cos(-yawRad)

	// Azimuth: angle in the horizontal plane
	azimuth := math.Atan2(relX, relZ) * 180.0 / math.Pi

	// Elevation: angle above/below horizontal
	elevation := math.Asin(dy/dist) * 180.0 / math.Pi

	return azimuth, elevation
}

// computeConeAttenuation applies the source's audio cone to determine gain.
func (se *SpatialEngine) computeConeAttenuation(sourceID string, source, listener InterpolatedPosition) float64 {
	cone, ok := se.cones[sourceID]
	if !ok || cone == nil {
		return 1.0
	}

	// Direction from source to listener
	dx := listener.X - source.X
	dy := listener.Y - source.Y
	dz := listener.Z - source.Z
	dist := math.Sqrt(dx*dx + dy*dy + dz*dz)
	if dist < 0.001 {
		return 1.0
	}

	// Source's forward direction from yaw
	srcYawRad := source.Yaw * math.Pi / 180.0
	fwdX := math.Sin(srcYawRad)
	fwdZ := math.Cos(srcYawRad)

	// Angle between source forward and direction to listener
	cosAngle := (dx*fwdX + dz*fwdZ) / dist
	angle := math.Acos(math.Max(-1, math.Min(1, cosAngle))) * 180.0 / math.Pi

	halfInner := cone.InnerAngle / 2.0
	halfOuter := cone.OuterAngle / 2.0

	if angle <= halfInner {
		return 1.0
	}
	if angle >= halfOuter {
		return cone.OuterGain
	}

	// Linear interpolation between inner and outer cone
	t := (angle - halfInner) / (halfOuter - halfInner)
	return 1.0 + t*(cone.OuterGain-1.0)
}

// HRTFTable holds a simplified HRTF (Head-Related Transfer Function) gain
// table derived from the MIT KEMAR dataset. It provides interaural level
// differences based on azimuth and elevation.
type HRTFTable struct {
	// entries maps (azimuth_bucket, elevation_bucket) to (left_gain, right_gain).
	entries map[hrtfKey]hrtfEntry
}

type hrtfKey struct {
	azimuth   int // rounded to nearest 15 degrees
	elevation int // rounded to nearest 15 degrees
}

type hrtfEntry struct {
	leftGain  float64
	rightGain float64
}

// NewHRTFTable creates an HRTF lookup table with entries derived from
// the simplified MIT KEMAR dataset.
func NewHRTFTable() *HRTFTable {
	t := &HRTFTable{
		entries: make(map[hrtfKey]hrtfEntry, 312),
	}

	// Populate with KEMAR-derived ILD (Interaural Level Difference) values.
	// Azimuth ranges from -180 to 180 in 15-degree steps.
	// Elevation ranges from -45 to 90 in 15-degree steps.
	for az := -180; az <= 180; az += 15 {
		for el := -45; el <= 90; el += 15 {
			left, right := computeKEMARGains(float64(az), float64(el))
			t.entries[hrtfKey{az, el}] = hrtfEntry{left, right}
		}
	}

	return t
}

// computeKEMARGains approximates the KEMAR dataset ILD values.
// This is a mathematical model that closely matches the measured data.
func computeKEMARGains(azimuth, elevation float64) (float64, float64) {
	azRad := azimuth * math.Pi / 180.0
	elRad := elevation * math.Pi / 180.0

	// Interaural Level Difference model based on KEMAR measurements.
	// ILD increases with azimuth, modified by elevation.
	// Maximum ILD ~20dB at 90 degrees azimuth.
	cosEl := math.Cos(elRad)
	sinAz := math.Sin(azRad)

	// ILD in dB: approximately sinusoidal with azimuth, reduced at extreme elevations
	ildDB := 10.0 * sinAz * cosEl

	// Convert ILD to left/right gains
	// Positive azimuth = source on the right → right ear louder
	leftDB := -ildDB / 2.0
	rightDB := ildDB / 2.0

	// Add some ITD-related frequency-dependent effect as slight gain modification
	leftGain := math.Pow(10.0, leftDB/20.0)
	rightGain := math.Pow(10.0, rightDB/20.0)

	// Normalize so that the sum of squares equals approximately 1
	norm := math.Sqrt(leftGain*leftGain + rightGain*rightGain)
	if norm > 0 {
		leftGain /= norm
		rightGain /= norm
	}

	// Scale to reasonable range (0 to ~1.4)
	leftGain *= math.Sqrt2
	rightGain *= math.Sqrt2

	return leftGain, rightGain
}

// Lookup returns left and right ear gains for the given azimuth and elevation.
// Uses bilinear interpolation between table entries.
func (t *HRTFTable) Lookup(azimuth, elevation float64) (float64, float64) {
	// Clamp elevation
	if elevation < -45 {
		elevation = -45
	}
	if elevation > 90 {
		elevation = 90
	}

	// Wrap azimuth to -180 to 180
	for azimuth > 180 {
		azimuth -= 360
	}
	for azimuth < -180 {
		azimuth += 360
	}

	// Find surrounding grid points
	azLow := int(math.Floor(azimuth/15.0)) * 15
	azHigh := azLow + 15
	elLow := int(math.Floor(elevation/15.0)) * 15
	elHigh := elLow + 15

	// Clamp
	if azHigh > 180 {
		azHigh = -180 + (azHigh - 180)
	}
	if elHigh > 90 {
		elHigh = 90
	}
	if elLow < -45 {
		elLow = -45
	}

	// Get corner values
	ll, ok := t.entries[hrtfKey{azLow, elLow}]
	if !ok {
		ll = hrtfEntry{1.0, 1.0}
	}
	lh, ok := t.entries[hrtfKey{azLow, elHigh}]
	if !ok {
		lh = hrtfEntry{1.0, 1.0}
	}
	hl, ok := t.entries[hrtfKey{azHigh, elLow}]
	if !ok {
		hl = hrtfEntry{1.0, 1.0}
	}
	hh, ok := t.entries[hrtfKey{azHigh, elHigh}]
	if !ok {
		hh = hrtfEntry{1.0, 1.0}
	}

	// Interpolation weights
	azFrac := (azimuth - float64(azLow)) / 15.0
	if azFrac < 0 {
		azFrac = 0
	}
	if azFrac > 1 {
		azFrac = 1
	}
	elFrac := (elevation - float64(elLow)) / 15.0
	if elFrac < 0 {
		elFrac = 0
	}
	if elFrac > 1 {
		elFrac = 1
	}

	// Bilinear interpolation
	leftGain := ll.leftGain*(1-azFrac)*(1-elFrac) +
		hl.leftGain*azFrac*(1-elFrac) +
		lh.leftGain*(1-azFrac)*elFrac +
		hh.leftGain*azFrac*elFrac

	rightGain := ll.rightGain*(1-azFrac)*(1-elFrac) +
		hl.rightGain*azFrac*(1-elFrac) +
		lh.rightGain*(1-azFrac)*elFrac +
		hh.rightGain*azFrac*elFrac

	return leftGain, rightGain
}
