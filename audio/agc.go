package audio

import (
	"math"
	"sync"
)

// AGC implements Automatic Gain Control that adjusts audio levels to a target
// dBFS value. It uses envelope tracking with configurable attack and release
// times and includes a peak limiter to prevent clipping.
type AGC struct {
	// targetLevel is the desired output level in dBFS (e.g., -18).
	targetLevel float64
	// attackTimeMs is how quickly the gain responds to increases in level.
	attackTimeMs float64
	// releaseTimeMs is how quickly the gain responds to decreases in level.
	releaseTimeMs float64
	// maxGainDB is the maximum gain that can be applied.
	maxGainDB float64
	// limiterThresholdDB is the peak limiter threshold in dBFS.
	limiterThresholdDB float64
	// sampleRate is the audio sample rate.
	sampleRate int

	// currentGainDB is the current gain being applied.
	currentGainDB float64
	// envelopeDB tracks the input signal envelope in dB.
	envelopeDB float64

	// attackCoeff and releaseCoeff are the smoothing coefficients.
	attackCoeff  float64
	releaseCoeff float64

	// mu protects all mutable state.
	mu sync.Mutex
}

// NewAGC creates a new Automatic Gain Control processor.
// targetDBFS is the desired output level (e.g., -18 for -18 dBFS).
// sampleRate is the audio sample rate in Hz.
func NewAGC(targetDBFS int, sampleRate int) *AGC {
	attackMs := 5.0
	releaseMs := 50.0

	a := &AGC{
		targetLevel:        float64(targetDBFS),
		attackTimeMs:       attackMs,
		releaseTimeMs:      releaseMs,
		maxGainDB:          30.0,
		limiterThresholdDB: -1.0,
		sampleRate:         sampleRate,
		currentGainDB:      0.0,
		envelopeDB:         -96.0,
	}

	// Compute smoothing coefficients from time constants
	// coeff = 1 - exp(-1 / (timeMs * sampleRate / 1000))
	samplesPerMs := float64(sampleRate) / 1000.0
	a.attackCoeff = 1.0 - math.Exp(-1.0/(attackMs*samplesPerMs))
	a.releaseCoeff = 1.0 - math.Exp(-1.0/(releaseMs*samplesPerMs))

	return a
}

// Process applies automatic gain control to a frame of 16-bit PCM samples.
// The samples are modified in place. Returns the gain in dB that was applied.
func (a *AGC) Process(samples []int16) float64 {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(samples) == 0 {
		return a.currentGainDB
	}

	// Compute input RMS level
	rms := computeRMS(samples)
	inputDB := linearToDB(rms)

	// Update envelope with attack/release
	if inputDB > a.envelopeDB {
		a.envelopeDB += a.attackCoeff * (inputDB - a.envelopeDB)
	} else {
		a.envelopeDB += a.releaseCoeff * (inputDB - a.envelopeDB)
	}

	// Compute desired gain
	desiredGainDB := a.targetLevel - a.envelopeDB

	// Clamp gain to maximum
	if desiredGainDB > a.maxGainDB {
		desiredGainDB = a.maxGainDB
	}
	if desiredGainDB < -a.maxGainDB {
		desiredGainDB = -a.maxGainDB
	}

	// Smooth gain transition
	gainDiff := desiredGainDB - a.currentGainDB
	if math.Abs(gainDiff) < 0.1 {
		a.currentGainDB = desiredGainDB
	} else {
		a.currentGainDB += 0.1 * gainDiff
	}

	// Apply gain to samples
	gainLinear := dbToLinear(a.currentGainDB)
	for i, s := range samples {
		val := float64(s) * gainLinear

		// Peak limiter: soft clipping
		if val > 32767.0*dbToLinear(a.limiterThresholdDB) {
			val = softClip(val, 32767.0)
		} else if val < -32767.0*dbToLinear(a.limiterThresholdDB) {
			val = -softClip(-val, 32767.0)
		}

		// Clamp to int16 range
		if val > 32767.0 {
			val = 32767.0
		} else if val < -32768.0 {
			val = -32768.0
		}
		samples[i] = int16(val)
	}

	return a.currentGainDB
}

// GetCurrentGain returns the current gain in dB being applied.
func (a *AGC) GetCurrentGain() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.currentGainDB
}

// Reset resets the AGC state.
func (a *AGC) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.currentGainDB = 0.0
	a.envelopeDB = -96.0
}

// computeRMS computes the root mean square of PCM samples.
func computeRMS(samples []int16) float64 {
	sum := 0.0
	for _, s := range samples {
		f := float64(s)
		sum += f * f
	}
	return math.Sqrt(sum / float64(len(samples)))
}

// linearToDB converts a linear amplitude value to decibels.
func linearToDB(linear float64) float64 {
	if linear < 1e-10 {
		return -96.0
	}
	return 20.0 * math.Log10(linear/32768.0)
}

// dbToLinear converts a decibel value to linear amplitude.
func dbToLinear(db float64) float64 {
	return math.Pow(10.0, db/20.0)
}

// softClip applies a soft clipping function to prevent harsh distortion.
func softClip(input, threshold float64) float64 {
	if input <= threshold*0.75 {
		return input
	}
	x := (input - threshold*0.75) / (threshold * 0.25)
	return threshold*0.75 + threshold*0.25*(2.0*x-x*x)/(1.0+x)
}