package audio

import (
	"math"
	"sync"
)

// NoiseLevel controls the noise suppression aggressiveness.
type NoiseLevel int

const (
	// NoiseLow applies minimal noise suppression.
	NoiseLow NoiseLevel = 0
	// NoiseMedium applies moderate noise suppression.
	NoiseMedium NoiseLevel = 1
	// NoiseHigh applies strong noise suppression.
	NoiseHigh NoiseLevel = 2
	// NoiseVeryHigh applies maximum noise suppression.
	NoiseVeryHigh NoiseLevel = 3
)

// NoiseSuppressor applies spectral subtraction based noise suppression.
// It estimates the noise floor from silent periods and subtracts it from
// the signal spectrum to reduce background noise.
type NoiseSuppressor struct {
	level      NoiseLevel
	sampleRate int
	fftSize    int

	// noiseEstimate holds the estimated noise power spectrum.
	noiseEstimate []float64
	// adaptRate controls how fast the noise estimate adapts.
	adaptRate float64
	// suppressionFactor is the amount of noise to subtract (0 to 1).
	suppressionFactor float64
	// initialized tracks whether the noise estimate has been initialised.
	initialized bool
	// frameCount for initialization period.
	frameCount int

	// mu protects all mutable state.
	mu sync.Mutex
}

// NewNoiseSuppressor creates a noise suppression processor with the specified
// aggressiveness level and sample rate.
func NewNoiseSuppressor(level NoiseLevel, sampleRate int) *NoiseSuppressor {
	fftSize := 256

	adaptRates := [4]float64{0.05, 0.03, 0.02, 0.01}
	suppFactors := [4]float64{0.5, 1.0, 1.5, 2.0}

	ns := &NoiseSuppressor{
		level:             level,
		sampleRate:        sampleRate,
		fftSize:           fftSize,
		noiseEstimate:     make([]float64, fftSize/2+1),
		adaptRate:         adaptRates[level],
		suppressionFactor: suppFactors[level],
	}

	return ns
}

// Process applies noise suppression to a frame of 16-bit PCM samples.
// Samples are modified in place.
func (ns *NoiseSuppressor) Process(samples []int16) {
	ns.mu.Lock()
	defer ns.mu.Unlock()

	n := len(samples)
	if n == 0 {
		return
	}

	ns.frameCount++

	// Convert to float64
	floatSamples := make([]float64, n)
	for i, s := range samples {
		floatSamples[i] = float64(s)
	}

	// Compute power spectrum using simple DFT bins
	specSize := ns.fftSize/2 + 1
	if specSize > len(ns.noiseEstimate) {
		specSize = len(ns.noiseEstimate)
	}

	powerSpectrum := make([]float64, specSize)
	for k := 0; k < specSize; k++ {
		freq := float64(k) * float64(ns.sampleRate) / float64(ns.fftSize)
		re, im := ns.computeDFTBin(floatSamples, freq)
		powerSpectrum[k] = re*re + im*im
	}

	// Initialize noise estimate from first few frames (assumed to be noise)
	if ns.frameCount <= 10 {
		for k := 0; k < specSize; k++ {
			if !ns.initialized {
				ns.noiseEstimate[k] = powerSpectrum[k]
			} else {
				ns.noiseEstimate[k] = 0.9*ns.noiseEstimate[k] + 0.1*powerSpectrum[k]
			}
		}
		ns.initialized = true
		return
	}

	// Update noise estimate during silence (minimum statistics approach)
	for k := 0; k < specSize; k++ {
		if powerSpectrum[k] < ns.noiseEstimate[k]*3.0 {
			ns.noiseEstimate[k] += ns.adaptRate * (powerSpectrum[k] - ns.noiseEstimate[k])
		}
	}

	// Compute spectral subtraction gain per band
	gains := make([]float64, specSize)
	for k := 0; k < specSize; k++ {
		snr := powerSpectrum[k] / (ns.noiseEstimate[k] + 1e-10)
		gain := 1.0 - ns.suppressionFactor/snr
		if gain < 0.05 {
			gain = 0.05 // Noise floor to prevent musical noise
		}
		gains[k] = math.Sqrt(gain)
	}

	// Apply gains in time domain using band-wise processing
	// Map each sample to its dominant frequency band and apply gain
	samplesPerBand := n / specSize
	if samplesPerBand < 1 {
		samplesPerBand = 1
	}

	for i := 0; i < n; i++ {
		bandIdx := (i * specSize) / n
		if bandIdx >= specSize {
			bandIdx = specSize - 1
		}
		floatSamples[i] *= gains[bandIdx]
	}

	// Convert back to int16
	for i := 0; i < n; i++ {
		val := floatSamples[i]
		if val > 32767 {
			val = 32767
		} else if val < -32768 {
			val = -32768
		}
		samples[i] = int16(val)
	}
}

// computeDFTBin computes the real and imaginary parts of a single DFT bin
// at the specified frequency.
func (ns *NoiseSuppressor) computeDFTBin(samples []float64, freq float64) (float64, float64) {
	n := len(samples)
	if n == 0 {
		return 0, 0
	}
	normalizedFreq := freq / float64(ns.sampleRate)
	w := 2.0 * math.Pi * normalizedFreq
	re, im := 0.0, 0.0
	for i := 0; i < n; i++ {
		angle := w * float64(i)
		re += samples[i] * math.Cos(angle)
		im -= samples[i] * math.Sin(angle)
	}
	return re / float64(n), im / float64(n)
}

// Reset resets the noise suppressor state.
func (ns *NoiseSuppressor) Reset() {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	for i := range ns.noiseEstimate {
		ns.noiseEstimate[i] = 0
	}
	ns.initialized = false
	ns.frameCount = 0
}