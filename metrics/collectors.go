package metrics

import (
	"math"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// RoomStats holds per-room statistics used by custom collectors.
type RoomStats struct {
	PacketsSent     uint64
	PacketsLost     uint64
	JitterMs        float64
	RoundTripTimeMs float64
}

// StatsProvider is an interface that room managers implement to provide
// per-room statistics for custom Prometheus collectors.
type StatsProvider interface {
	// GetAllRoomStats returns statistics for all active rooms.
	GetAllRoomStats() map[string]*RoomStats
}

// CustomCollector is a Prometheus collector that computes derived metrics
// such as MOS estimation, per-room jitter, and packet loss rate on scrape.
type CustomCollector struct {
	provider StatsProvider

	// mu protects the provider reference.
	mu sync.RWMutex

	mosDesc       *prometheus.Desc
	jitterDesc    *prometheus.Desc
	lossRateDesc  *prometheus.Desc
}

// NewCustomCollector creates a custom Prometheus collector.
// The provider parameter supplies room statistics on each scrape.
func NewCustomCollector(provider StatsProvider) *CustomCollector {
	c := &CustomCollector{
		provider: provider,
		mosDesc: prometheus.NewDesc(
			"nakama_voice_room_mos_estimate",
			"Estimated Mean Opinion Score per room.",
			[]string{"room_id"}, nil,
		),
		jitterDesc: prometheus.NewDesc(
			"nakama_voice_room_jitter_ms",
			"Current jitter in milliseconds per room.",
			[]string{"room_id"}, nil,
		),
		lossRateDesc: prometheus.NewDesc(
			"nakama_voice_room_loss_rate",
			"Current packet loss rate per room.",
			[]string{"room_id"}, nil,
		),
	}
	prometheus.MustRegister(c)
	return c
}

// Describe implements prometheus.Collector. It sends metric descriptors
// to the provided channel.
func (c *CustomCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.mosDesc
	ch <- c.jitterDesc
	ch <- c.lossRateDesc
}

// Collect implements prometheus.Collector. It computes derived metrics
// from the current room statistics and sends them to the provided channel.
func (c *CustomCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	provider := c.provider
	c.mu.RUnlock()

	if provider == nil {
		return
	}

	stats := provider.GetAllRoomStats()
	for roomID, s := range stats {
		lossRate := float64(0)
		if s.PacketsSent > 0 {
			lossRate = float64(s.PacketsLost) / float64(s.PacketsSent)
		}

		mos := estimateMOS(lossRate, s.JitterMs, s.RoundTripTimeMs)

		ch <- prometheus.MustNewConstMetric(c.mosDesc, prometheus.GaugeValue, mos, roomID)
		ch <- prometheus.MustNewConstMetric(c.jitterDesc, prometheus.GaugeValue, s.JitterMs, roomID)
		ch <- prometheus.MustNewConstMetric(c.lossRateDesc, prometheus.GaugeValue, lossRate, roomID)
	}
}

// estimateMOS computes an approximate MOS score using the E-model (ITU-T G.107).
// This is a simplified version suitable for real-time monitoring.
// Parameters: lossRate (0-1), jitterMs, rttMs.
func estimateMOS(lossRate, jitterMs, rttMs float64) float64 {
	// R-factor computation (simplified E-model)
	// Base R = 93.2 (narrowband reference)
	r := 93.2

	// Delay impairment: Id = 0.024*d + 0.11*(d-177.3)*H(d-177.3)
	effectiveDelay := rttMs/2.0 + jitterMs*2.0
	id := 0.024 * effectiveDelay
	if effectiveDelay > 177.3 {
		id += 0.11 * (effectiveDelay - 177.3)
	}

	// Equipment impairment for OPUS: Ie-eff
	// Using BPL = 25.1 for 32kbps OPUS (approximation)
	bpl := 25.1
	ie := 1.0 + bpl*math.Log(1.0+lossRate*100.0)

	r = r - id - ie

	// Clamp R to [0, 100]
	if r < 0 {
		r = 0
	}
	if r > 100 {
		r = 100
	}

	// Convert R to MOS
	if r < 6.5 {
		return 1.0
	}
	mos := 1.0 + 0.035*r + r*(r-60.0)*(100.0-r)*7.0e-6
	if mos > 5.0 {
		mos = 5.0
	}
	if mos < 1.0 {
		mos = 1.0
	}
	return mos
}
