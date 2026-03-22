package audio

// Downsample48to16 converts 48kHz mono PCM samples to 16kHz by taking
// every 3rd sample. This is a simple decimation without anti-aliasing
// filter — acceptable for VAD energy analysis but not for playback.
func Downsample48to16(samples48 []int16) []int16 {
	n := len(samples48) / 3
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		out[i] = samples48[i*3]
	}
	return out
}
