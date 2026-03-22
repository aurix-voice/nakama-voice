package moderation

// ContentFilter defines an interface for audio content filtering.
// Implementations can analyze audio content for policy violations.
// This is an extension point for future content moderation capabilities.
type ContentFilter interface {
	// Analyze examines audio data and returns a policy decision.
	// Returns true if the content is allowed, false if it should be blocked.
	Analyze(userID string, audioData []byte) (bool, error)

	// Name returns the filter's identifier for logging.
	Name() string
}

// NoopFilter is a content filter that allows all content.
// It serves as the default implementation.
type NoopFilter struct{}

// Analyze always returns true (content allowed) for the noop filter.
func (f *NoopFilter) Analyze(userID string, audioData []byte) (bool, error) {
	return true, nil
}

// Name returns the noop filter's identifier.
func (f *NoopFilter) Name() string {
	return "noop"
}

// FilterChain applies multiple content filters in sequence.
// Content is blocked if any filter rejects it.
type FilterChain struct {
	filters []ContentFilter
}

// NewFilterChain creates a filter chain with the given filters.
func NewFilterChain(filters ...ContentFilter) *FilterChain {
	return &FilterChain{filters: filters}
}

// Analyze runs all filters in the chain. Returns false if any filter rejects.
func (fc *FilterChain) Analyze(userID string, audioData []byte) (bool, string, error) {
	for _, f := range fc.filters {
		allowed, err := f.Analyze(userID, audioData)
		if err != nil {
			return false, f.Name(), err
		}
		if !allowed {
			return false, f.Name(), nil
		}
	}
	return true, "", nil
}
