package sfu

import (
	"math"
	"sync"
	"time"

	"github.com/pion/rtcp"
)

// BandwidthEstimator tracks available bandwidth per peer using REMB
// (Receiver Estimated Maximum Bitrate) and Transport-CC feedback.
// It implements probing pacing and per-peer bandwidth state management.
type BandwidthEstimator struct {
	// currentBitrateKbps is the current estimated available bitrate.
	currentBitrateKbps float64
	// maxBitrateKbps is the configured maximum bitrate cap.
	maxBitrateKbps float64
	// minBitrateKbps is the minimum bitrate floor.
	minBitrateKbps float64

	// REMB state
	lastREMBTime    time.Time
	lastREMBBitrate float64

	// Transport-CC state
	tccSeq            uint16
	tccSendTimes      map[uint16]time.Time
	tccRecvDeltas     []float64
	lastTCCFeedback   time.Time
	lossBasedEstimate float64
	delayBasedEstimate float64

	// Probing state
	probingActive bool
	probeBitrate  float64
	probeStart    time.Time

	// Smoothing
	smoothedBitrate float64

	// mu protects all mutable state.
	mu sync.Mutex
}

// NewBandwidthEstimator creates a bandwidth estimator with the specified
// maximum bitrate in kbps.
func NewBandwidthEstimator(maxBitrateKbps int) *BandwidthEstimator {
	initial := float64(maxBitrateKbps) * 0.75
	return &BandwidthEstimator{
		currentBitrateKbps: initial,
		maxBitrateKbps:     float64(maxBitrateKbps),
		minBitrateKbps:     16.0,
		smoothedBitrate:    initial,
		tccSendTimes:       make(map[uint16]time.Time, 256),
		lossBasedEstimate:  float64(maxBitrateKbps),
		delayBasedEstimate: float64(maxBitrateKbps),
	}
}

// OnREMB processes an incoming REMB packet and updates the bandwidth estimate.
// Returns the new estimated bitrate in kbps.
func (be *BandwidthEstimator) OnREMB(remb *rtcp.ReceiverEstimatedMaximumBitrate) float64 {
	be.mu.Lock()
	defer be.mu.Unlock()

	now := time.Now()
	bitsPerSec := float64(remb.Bitrate)
	kbps := bitsPerSec / 1000.0

	be.lastREMBTime = now
	be.lastREMBBitrate = kbps

	// Smooth the REMB estimate using exponential moving average
	alpha := 0.3
	if kbps < be.smoothedBitrate {
		alpha = 0.8 // React faster to decreases
	}
	be.smoothedBitrate = alpha*kbps + (1-alpha)*be.smoothedBitrate

	be.delayBasedEstimate = be.smoothedBitrate
	be.updateEstimate()

	return be.currentBitrateKbps
}

// OnTransportCCFeedback processes Transport-CC feedback and updates the
// bandwidth estimate based on inter-arrival delay variation.
func (be *BandwidthEstimator) OnTransportCCFeedback(fb *rtcp.TransportLayerCC) float64 {
	be.mu.Lock()
	defer be.mu.Unlock()

	now := time.Now()
	be.lastTCCFeedback = now

	// Process received packet statuses
	var recvDeltas []float64
	refTime := fb.ReferenceTime
	_ = refTime

	totalPackets := 0
	lostPackets := 0

	for _, chunk := range fb.PacketChunks {
		switch c := chunk.(type) {
		case *rtcp.RunLengthChunk:
			totalPackets += int(c.RunLength)
			if c.PacketStatus == rtcp.TypeTCCPacketNotReceived {
				lostPackets += int(c.RunLength)
			}
		case *rtcp.StatusVectorChunk:
			for _, status := range c.SymbolList {
				totalPackets++
				if status == rtcp.TypeTCCPacketNotReceived {
					lostPackets++
				}
			}
		}
	}

	// Extract receive deltas for delay-based estimation
	for _, delta := range fb.RecvDeltas {
		recvDeltas = append(recvDeltas, float64(delta.Delta)/250.0) // Convert to ms
	}
	be.tccRecvDeltas = append(be.tccRecvDeltas, recvDeltas...)

	// Keep only recent deltas
	maxDeltas := 100
	if len(be.tccRecvDeltas) > maxDeltas {
		be.tccRecvDeltas = be.tccRecvDeltas[len(be.tccRecvDeltas)-maxDeltas:]
	}

	// Loss-based estimation
	if totalPackets > 0 {
		lossRate := float64(lostPackets) / float64(totalPackets)
		if lossRate > 0.1 {
			be.lossBasedEstimate *= (1.0 - lossRate*0.5)
		} else if lossRate < 0.02 {
			be.lossBasedEstimate *= 1.05
		}
		if be.lossBasedEstimate < be.minBitrateKbps {
			be.lossBasedEstimate = be.minBitrateKbps
		}
		if be.lossBasedEstimate > be.maxBitrateKbps {
			be.lossBasedEstimate = be.maxBitrateKbps
		}
	}

	// Delay-based estimation using inter-arrival delay gradients
	if len(be.tccRecvDeltas) > 2 {
		delayGradient := be.computeDelayGradient()
		if delayGradient > 5.0 { // Increasing delay → congestion
			be.delayBasedEstimate *= 0.85
		} else if delayGradient < -1.0 { // Decreasing delay → capacity available
			be.delayBasedEstimate *= 1.05
		}
		if be.delayBasedEstimate < be.minBitrateKbps {
			be.delayBasedEstimate = be.minBitrateKbps
		}
		if be.delayBasedEstimate > be.maxBitrateKbps {
			be.delayBasedEstimate = be.maxBitrateKbps
		}
	}

	be.updateEstimate()
	return be.currentBitrateKbps
}

// RecordSend records that a packet was sent for Transport-CC tracking.
func (be *BandwidthEstimator) RecordSend(tccSeq uint16) {
	be.mu.Lock()
	defer be.mu.Unlock()
	be.tccSendTimes[tccSeq] = time.Now()

	// Prune old entries
	if len(be.tccSendTimes) > 500 {
		cutoff := time.Now().Add(-10 * time.Second)
		for seq, t := range be.tccSendTimes {
			if t.Before(cutoff) {
				delete(be.tccSendTimes, seq)
			}
		}
	}
}

// GetEstimatedBitrate returns the current estimated available bitrate in kbps.
func (be *BandwidthEstimator) GetEstimatedBitrate() float64 {
	be.mu.Lock()
	defer be.mu.Unlock()
	return be.currentBitrateKbps
}

// StartProbing initiates bandwidth probing at the specified target bitrate.
func (be *BandwidthEstimator) StartProbing(targetKbps float64) {
	be.mu.Lock()
	defer be.mu.Unlock()
	be.probingActive = true
	be.probeBitrate = targetKbps
	be.probeStart = time.Now()
}

// StopProbing ends the current probing session.
func (be *BandwidthEstimator) StopProbing() {
	be.mu.Lock()
	defer be.mu.Unlock()
	be.probingActive = false
}

// IsProbingActive returns whether bandwidth probing is currently active.
func (be *BandwidthEstimator) IsProbingActive() bool {
	be.mu.Lock()
	defer be.mu.Unlock()
	return be.probingActive
}

// Stats returns bandwidth estimation statistics.
func (be *BandwidthEstimator) Stats() BandwidthStats {
	be.mu.Lock()
	defer be.mu.Unlock()
	return BandwidthStats{
		EstimatedKbps:   be.currentBitrateKbps,
		LossBasedKbps:   be.lossBasedEstimate,
		DelayBasedKbps:  be.delayBasedEstimate,
		MaxKbps:         be.maxBitrateKbps,
		ProbingActive:   be.probingActive,
	}
}

// BandwidthStats holds bandwidth estimation statistics.
type BandwidthStats struct {
	EstimatedKbps  float64 `json:"estimated_kbps"`
	LossBasedKbps  float64 `json:"loss_based_kbps"`
	DelayBasedKbps float64 `json:"delay_based_kbps"`
	MaxKbps        float64 `json:"max_kbps"`
	ProbingActive  bool    `json:"probing_active"`
}

func (be *BandwidthEstimator) updateEstimate() {
	// Take the minimum of loss-based and delay-based estimates
	estimate := math.Min(be.lossBasedEstimate, be.delayBasedEstimate)
	if estimate < be.minBitrateKbps {
		estimate = be.minBitrateKbps
	}
	if estimate > be.maxBitrateKbps {
		estimate = be.maxBitrateKbps
	}
	be.currentBitrateKbps = estimate
}

func (be *BandwidthEstimator) computeDelayGradient() float64 {
	n := len(be.tccRecvDeltas)
	if n < 4 {
		return 0
	}
	// Compute trend using linear regression on recent deltas
	recentN := 20
	if n < recentN {
		recentN = n
	}
	recent := be.tccRecvDeltas[n-recentN:]

	sumX, sumY, sumXY, sumX2 := 0.0, 0.0, 0.0, 0.0
	for i, d := range recent {
		x := float64(i)
		sumX += x
		sumY += d
		sumXY += x * d
		sumX2 += x * x
	}
	fn := float64(recentN)
	denom := fn*sumX2 - sumX*sumX
	if math.Abs(denom) < 1e-10 {
		return 0
	}
	slope := (fn*sumXY - sumX*sumY) / denom
	return slope
}