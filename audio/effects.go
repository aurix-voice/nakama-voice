package audio

import (
	"sync"
)

// EffectType identifies a specific audio effect in the processing chain.
type EffectType int

const (
	// EffectMuteGate blocks all audio when active.
	EffectMuteGate EffectType = iota
	// EffectVolumeScale applies a volume multiplier.
	EffectVolumeScale
	// EffectNoiseSuppress applies noise suppression.
	EffectNoiseSuppress
	// EffectAGC applies automatic gain control.
	EffectAGC
)

// Effect represents a single audio processing stage.
type Effect struct {
	Type    EffectType
	Enabled bool
	Params  map[string]float64
}

// EffectsChain manages an ordered chain of audio effects per user.
// Effects are applied in order: mute gate → noise suppression → AGC → volume scale.
type EffectsChain struct {
	effects []*Effect

	// Dedicated processors
	agc             *AGC
	noiseSuppressor *NoiseSuppressor

	// muteActive indicates whether the mute gate is active.
	muteActive bool
	// volumeScale is the volume multiplier (1.0 = no change).
	volumeScale float64

	// mu protects all mutable state in the chain.
	mu sync.Mutex
}

// NewEffectsChain creates a new per-user audio effects chain with default settings.
// sampleRate is the audio sample rate in Hz.
func NewEffectsChain(sampleRate int) *EffectsChain {
	return &EffectsChain{
		effects: []*Effect{
			{Type: EffectMuteGate, Enabled: false, Params: map[string]float64{}},
			{Type: EffectNoiseSuppress, Enabled: false, Params: map[string]float64{"level": 1}},
			{Type: EffectAGC, Enabled: false, Params: map[string]float64{"target_dbfs": -18}},
			{Type: EffectVolumeScale, Enabled: true, Params: map[string]float64{"scale": 1.0}},
		},
		agc:             NewAGC(-18, sampleRate),
		noiseSuppressor: NewNoiseSuppressor(NoiseMedium, sampleRate),
		volumeScale:     1.0,
	}
}

// Process applies the effects chain to a frame of PCM audio samples.
// Samples are modified in place. Returns true if audio was passed through
// (not muted), false if muted.
func (ec *EffectsChain) Process(samples []int16) bool {
	ec.mu.Lock()
	defer ec.mu.Unlock()

	if len(samples) == 0 {
		return !ec.muteActive
	}

	for _, effect := range ec.effects {
		if !effect.Enabled {
			continue
		}

		switch effect.Type {
		case EffectMuteGate:
			if ec.muteActive {
				for i := range samples {
					samples[i] = 0
				}
				return false
			}

		case EffectNoiseSuppress:
			ec.noiseSuppressor.Process(samples)

		case EffectAGC:
			ec.agc.Process(samples)

		case EffectVolumeScale:
			scale := ec.volumeScale
			for i, s := range samples {
				val := float64(s) * scale
				if val > 32767 {
					val = 32767
				} else if val < -32768 {
					val = -32768
				}
				samples[i] = int16(val)
			}
		}
	}

	return true
}

// SetMuted enables or disables the mute gate.
func (ec *EffectsChain) SetMuted(muted bool) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.muteActive = muted
	for _, e := range ec.effects {
		if e.Type == EffectMuteGate {
			e.Enabled = muted
			break
		}
	}
}

// IsMuted returns whether the mute gate is active.
func (ec *EffectsChain) IsMuted() bool {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	return ec.muteActive
}

// SetVolume sets the volume scale factor. 1.0 is unity gain.
func (ec *EffectsChain) SetVolume(scale float64) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	if scale < 0 {
		scale = 0
	}
	if scale > 5.0 {
		scale = 5.0
	}
	ec.volumeScale = scale
}

// EnableEffect enables or disables a specific effect type.
func (ec *EffectsChain) EnableEffect(effectType EffectType, enabled bool) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	for _, e := range ec.effects {
		if e.Type == effectType {
			e.Enabled = enabled
			break
		}
	}
}

// Reset restores all effects to default state.
func (ec *EffectsChain) Reset() {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.muteActive = false
	ec.volumeScale = 1.0
	ec.agc.Reset()
	ec.noiseSuppressor.Reset()
	for _, e := range ec.effects {
		e.Enabled = e.Type == EffectVolumeScale
	}
}
