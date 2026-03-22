// Package audio implements audio processing components including Voice Activity
// Detection, Automatic Gain Control, noise suppression, and spatial audio.
package audio

import (
	"math"
	"sync"
)

// VADAggressiveness controls the sensitivity of Voice Activity Detection.
// Higher values are more aggressive at classifying input as silence.
type VADAggressiveness int

const (
	// VADQuality is the least aggressive mode — more sensitive to speech.
	VADQuality VADAggressiveness = 0
	// VADLowBitrate is slightly more aggressive.
	VADLowBitrate VADAggressiveness = 1
	// VADAggressive is moderately aggressive.
	VADAggressive VADAggressiveness = 2
	// VADVeryAggressive is the most aggressive — classifies more as silence.
	VADVeryAggressive VADAggressiveness = 3
)

// VAD implements a Gaussian Mixture Model based Voice Activity Detection engine
// inspired by the WebRTC VAD algorithm. It operates on 16-bit PCM audio frames
// at 16kHz sample rate and uses mel-frequency filterbank energy features for
// classification.
type VAD struct {
	aggressiveness VADAggressiveness
	sampleRate     int
	frameSizeMs    int

	// hangoverFrames is the number of frames to keep reporting speech after
	// the last speech frame, to prevent rapid toggling.
	hangoverFrames int
	hangoverCount  int

	// hysteresis prevents flicker by requiring consecutive speech/silence
	// classifications before changing state.
	hysteresisUp   int
	hysteresisDown int
	speechCount    int
	silenceCount   int

	// isSpeaking tracks the current VAD state.
	isSpeaking bool

	// GMM parameters for speech and noise models.
	speechMean     [numFilterBands]float64
	speechVar      [numFilterBands]float64
	noiseMean      [numFilterBands]float64
	noiseVar       [numFilterBands]float64

	// Energy threshold per aggressiveness level.
	energyThreshold float64

	// frameCount tracks processed frames for model adaptation.
	frameCount int64

	// mu protects all mutable state.
	mu sync.Mutex
}

const (
	numFilterBands = 6
	vadSampleRate  = 16000
)

// Thresholds for each aggressiveness level: energy threshold in dB.
var vadThresholds = [4]float64{
	15.0, // Quality: low threshold, more speech detected
	20.0, // LowBitrate
	25.0, // Aggressive
	30.0, // VeryAggressive: high threshold, less speech detected
}

// NewVAD creates a new Voice Activity Detection engine with the specified
// aggressiveness mode and hangover duration in milliseconds.
func NewVAD(aggressiveness VADAggressiveness, hangoverMs int) *VAD {
	if aggressiveness < VADQuality || aggressiveness > VADVeryAggressive {
		aggressiveness = VADAggressive
	}

	frameSizeMs := 20
	framesPerMs := 1000 / frameSizeMs
	hangoverFrames := (hangoverMs * framesPerMs) / 1000
	if hangoverFrames < 1 {
		hangoverFrames = 1
	}

	v := &VAD{
		aggressiveness:  aggressiveness,
		sampleRate:      vadSampleRate,
		frameSizeMs:     frameSizeMs,
		hangoverFrames:  hangoverFrames,
		hysteresisUp:    3,
		hysteresisDown:  10,
		energyThreshold: vadThresholds[aggressiveness],
	}

	// Initialize GMM parameters with reasonable defaults
	for i := 0; i < numFilterBands; i++ {
		v.speechMean[i] = 30.0 + float64(i)*2.0
		v.speechVar[i] = 100.0
		v.noiseMean[i] = 10.0
		v.noiseVar[i] = 50.0
	}

	return v
}

// ProcessFrame analyzes a single audio frame and returns true if speech is
// detected. The input should be 16-bit PCM samples at 16kHz. The frame length
// should be 10ms (160 samples), 20ms (320 samples), or 30ms (480 samples).
func (v *VAD) ProcessFrame(samples []int16) bool {
	v.mu.Lock()
	defer v.mu.Unlock()

	if len(samples) == 0 {
		return v.isSpeaking
	}

	v.frameCount++

	// Compute mel-frequency filterbank energies
	bandEnergies := v.computeFilterbankEnergies(samples)

	// Compute log-likelihood ratio for speech vs noise using GMM
	speechLLR := v.computeGMMLikelihood(bandEnergies)

	// Apply energy threshold
	totalEnergy := v.computeFrameEnergy(samples)
	energyDB := 10.0 * math.Log10(totalEnergy+1e-10)

	frameSpeech := speechLLR > 0 && energyDB > v.energyThreshold

	// Apply hysteresis
	if frameSpeech {
		v.speechCount++
		v.silenceCount = 0
	} else {
		v.silenceCount++
		v.speechCount = 0
	}

	previousState := v.isSpeaking

	if !v.isSpeaking && v.speechCount >= v.hysteresisUp {
		v.isSpeaking = true
		v.hangoverCount = v.hangoverFrames
	} else if v.isSpeaking && v.silenceCount >= v.hysteresisDown {
		if v.hangoverCount > 0 {
			v.hangoverCount--
		} else {
			v.isSpeaking = false
		}
	}

	// Adapt GMM models slowly
	if v.frameCount > 100 {
		v.adaptModels(bandEnergies, frameSpeech)
	}

	_ = previousState // Available for logging if needed

	return v.isSpeaking
}

// IsSpeaking returns the current VAD state without processing a new frame.
func (v *VAD) IsSpeaking() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.isSpeaking
}

// Reset resets the VAD state to its initial configuration.
func (v *VAD) Reset() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.isSpeaking = false
	v.hangoverCount = 0
	v.speechCount = 0
	v.silenceCount = 0
	v.frameCount = 0
}

// computeFilterbankEnergies computes mel-scale filterbank energies for the frame.
func (v *VAD) computeFilterbankEnergies(samples []int16) [numFilterBands]float64 {
	var energies [numFilterBands]float64
	n := len(samples)
	if n == 0 {
		return energies
	}

	// Simple triangular filterbank approach on spectral magnitudes
	// Compute a basic DFT magnitude spectrum (using Goertzel for efficiency)
	nfft := 256
	if n < nfft {
		nfft = n
	}

	// Band boundaries in FFT bins (for 16kHz, 256-point FFT → bin resolution = 62.5 Hz)
	// Mel bands approximate: 0-250, 250-500, 500-1000, 1000-2000, 2000-4000, 4000-8000
	bandBins := [numFilterBands + 1]int{0, 4, 8, 16, 32, 64, 128}

	// Compute power spectrum using Goertzel algorithm for each band center
	for band := 0; band < numFilterBands; band++ {
		startBin := bandBins[band]
		endBin := bandBins[band+1]
		if endBin > nfft/2 {
			endBin = nfft / 2
		}

		bandPower := 0.0
		for k := startBin; k < endBin; k++ {
			freq := float64(k) * float64(v.sampleRate) / float64(nfft)
			power := v.goertzelPower(samples, freq, nfft)
			bandPower += power
		}

		numBins := endBin - startBin
		if numBins < 1 {
			numBins = 1
		}
		energies[band] = bandPower / float64(numBins)
	}

	return energies
}

// goertzelPower computes the power at a specific frequency using the Goertzel algorithm.
func (v *VAD) goertzelPower(samples []int16, targetFreq float64, n int) float64 {
	if n > len(samples) {
		n = len(samples)
	}
	normalizedFreq := targetFreq / float64(v.sampleRate)
	coeff := 2.0 * math.Cos(2.0*math.Pi*normalizedFreq)

	s0, s1, s2 := 0.0, 0.0, 0.0
	for i := 0; i < n; i++ {
		s0 = float64(samples[i]) + coeff*s1 - s2
		s2 = s1
		s1 = s0
	}

	power := s1*s1 + s2*s2 - coeff*s1*s2
	if power < 0 {
		power = 0
	}
	return power / float64(n*n)
}

// computeGMMLikelihood computes the log-likelihood ratio of speech vs noise
// using Gaussian Mixture Model parameters.
func (v *VAD) computeGMMLikelihood(bandEnergies [numFilterBands]float64) float64 {
	llr := 0.0
	for i := 0; i < numFilterBands; i++ {
		energy := math.Log(bandEnergies[i] + 1e-10)

		// Speech likelihood (Gaussian)
		speechDiff := energy - v.speechMean[i]
		speechLL := -0.5*math.Log(2.0*math.Pi*v.speechVar[i]) -
			0.5*speechDiff*speechDiff/v.speechVar[i]

		// Noise likelihood (Gaussian)
		noiseDiff := energy - v.noiseMean[i]
		noiseLL := -0.5*math.Log(2.0*math.Pi*v.noiseVar[i]) -
			0.5*noiseDiff*noiseDiff/v.noiseVar[i]

		llr += speechLL - noiseLL
	}
	return llr
}

// computeFrameEnergy computes the total energy of the frame.
func (v *VAD) computeFrameEnergy(samples []int16) float64 {
	energy := 0.0
	for _, s := range samples {
		f := float64(s)
		energy += f * f
	}
	return energy / float64(len(samples))
}

// adaptModels slowly adapts the GMM speech and noise models based on
// classification results. Uses exponential moving average.
func (v *VAD) adaptModels(bandEnergies [numFilterBands]float64, isSpeech bool) {
	const adaptRate = 0.001
	for i := 0; i < numFilterBands; i++ {
		energy := math.Log(bandEnergies[i] + 1e-10)
		if isSpeech {
			v.speechMean[i] += adaptRate * (energy - v.speechMean[i])
			diff := energy - v.speechMean[i]
			v.speechVar[i] += adaptRate * (diff*diff - v.speechVar[i])
			if v.speechVar[i] < 1.0 {
				v.speechVar[i] = 1.0
			}
		} else {
			v.noiseMean[i] += adaptRate * (energy - v.noiseMean[i])
			diff := energy - v.noiseMean[i]
			v.noiseVar[i] += adaptRate * (diff*diff - v.noiseVar[i])
			if v.noiseVar[i] < 1.0 {
				v.noiseVar[i] = 1.0
			}
		}
	}
}