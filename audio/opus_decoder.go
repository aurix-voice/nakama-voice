// Package audio — opus_decoder.go provides OPUS to PCM decoding for the
// VAD and audio processing pipeline. It wraps the gopkg.in/hraban/opus.v2
// decoder with per-user lifecycle management.
package audio

import (
	"fmt"
	"sync"

	"gopkg.in/hraban/opus.v2"
)

// OPUSDecoder wraps an OPUS decoder instance for a single audio stream.
// It decodes OPUS-encoded RTP payloads into 16-bit PCM samples suitable
// for VAD, AGC, and noise suppression processing.
type OPUSDecoder struct {
	decoder    *opus.Decoder
	sampleRate int
	channels   int
	// pcmBuf is a reusable buffer for decoded PCM samples.
	pcmBuf []int16
	mu     sync.Mutex
}

// NewOPUSDecoder creates an OPUS decoder for the given sample rate and channel count.
// Typical values: sampleRate=48000, channels=2 (stereo) or 1 (mono).
func NewOPUSDecoder(sampleRate, channels int) (*OPUSDecoder, error) {
	dec, err := opus.NewDecoder(sampleRate, channels)
	if err != nil {
		return nil, fmt.Errorf("audio: create opus decoder: %w", err)
	}
	// Max frame size: 120ms at 48kHz = 5760 samples per channel
	maxSamples := 5760 * channels
	return &OPUSDecoder{
		decoder:    dec,
		sampleRate: sampleRate,
		channels:   channels,
		pcmBuf:     make([]int16, maxSamples),
	}, nil
}

// Decode decodes an OPUS packet into PCM int16 samples.
// Returns the decoded samples (slice of the internal buffer — copy if you
// need to retain across calls). The returned samples are interleaved if
// channels > 1.
func (d *OPUSDecoder) Decode(opusData []byte) ([]int16, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	n, err := d.decoder.Decode(opusData, d.pcmBuf)
	if err != nil {
		return nil, fmt.Errorf("audio: opus decode: %w", err)
	}
	// n is the number of samples per channel
	totalSamples := n * d.channels
	return d.pcmBuf[:totalSamples], nil
}

// DecodeMono decodes an OPUS packet and downmixes to mono int16 samples.
// This is the format expected by VAD (16kHz mono). If the decoder is stereo,
// channels are averaged. If the sample rate differs from 16kHz, the caller
// must resample separately.
func (d *OPUSDecoder) DecodeMono(opusData []byte) ([]int16, error) {
	samples, err := d.Decode(opusData)
	if err != nil {
		return nil, err
	}
	if d.channels == 1 {
		return samples, nil
	}
	// Downmix stereo to mono by averaging
	monoLen := len(samples) / d.channels
	mono := make([]int16, monoLen)
	for i := 0; i < monoLen; i++ {
		sum := int32(0)
		for ch := 0; ch < d.channels; ch++ {
			sum += int32(samples[i*d.channels+ch])
		}
		mono[i] = int16(sum / int32(d.channels))
	}
	return mono, nil
}

// Reset resets the decoder state (e.g., after packet loss).
func (d *OPUSDecoder) Reset() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.decoder.ResetState()
}

// SampleRate returns the decoder's sample rate.
func (d *OPUSDecoder) SampleRate() int { return d.sampleRate }

// Channels returns the decoder's channel count.
func (d *OPUSDecoder) Channels() int { return d.channels }
