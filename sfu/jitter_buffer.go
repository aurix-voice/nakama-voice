// Package sfu implements the Selective Forwarding Unit responsible for WebRTC
// peer connection management and RTP audio routing.
package sfu

import (
	"math"
	"sync"
	"time"
)

// JitterBuffer implements an adaptive jitter buffer that reorders out-of-order
// RTP packets and smooths delivery timing. It uses RFC 3550 jitter calculation
// to dynamically adjust buffer depth.
type JitterBuffer struct {
	// capacity is the maximum number of packets the buffer can hold.
	capacity int
	// minDelayMs is the minimum buffer delay in milliseconds.
	minDelayMs float64
	// maxDelayMs is the maximum buffer delay in milliseconds.
	maxDelayMs float64
	// currentDelayMs is the current adaptive delay target.
	currentDelayMs float64

	// packets holds buffered packets indexed by sequence number.
	packets map[uint16]*BufferedPacket
	// nextSeq is the next expected sequence number.
	nextSeq uint16
	// initialized tracks whether we've received the first packet.
	initialized bool

	// Jitter estimation (RFC 3550)
	lastTransit   int64
	jitterEstimate float64
	// packetsReceived counts total packets received.
	packetsReceived uint64
	// packetsLost counts detected packet losses.
	packetsLost uint64
	// lastReceiveTime is the wall-clock time of last packet receipt.
	lastReceiveTime time.Time

	// mu protects all mutable state.
	mu sync.Mutex
}

// BufferedPacket holds an RTP packet in the jitter buffer.
type BufferedPacket struct {
	Data       []byte
	SeqNum     uint16
	Timestamp  uint32
	ReceivedAt time.Time
}

// NewJitterBuffer creates an adaptive jitter buffer with the specified capacity
// and delay bounds in milliseconds.
func NewJitterBuffer(capacity int, minDelayMs, maxDelayMs float64) *JitterBuffer {
	return &JitterBuffer{
		capacity:       capacity,
		minDelayMs:     minDelayMs,
		maxDelayMs:     maxDelayMs,
		currentDelayMs: minDelayMs,
		packets:        make(map[uint16]*BufferedPacket, capacity),
	}
}

// Push adds an RTP packet to the jitter buffer. Returns true if the packet
// was accepted, false if it was a duplicate or the buffer is full.
func (jb *JitterBuffer) Push(data []byte, seqNum uint16, timestamp uint32) bool {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	now := time.Now()
	jb.packetsReceived++

	if !jb.initialized {
		jb.nextSeq = seqNum
		jb.initialized = true
		jb.lastReceiveTime = now
	}

	// Duplicate check
	if _, exists := jb.packets[seqNum]; exists {
		return false
	}

	// Buffer full check
	if len(jb.packets) >= jb.capacity {
		// Drop oldest packet
		jb.dropOldest()
	}

	// Update jitter estimate (RFC 3550)
	transit := now.UnixMilli() - int64(timestamp/8) // Assuming 8kHz timestamp clock
	if jb.lastTransit != 0 {
		d := math.Abs(float64(transit - jb.lastTransit))
		jb.jitterEstimate += (d - jb.jitterEstimate) / 16.0
	}
	jb.lastTransit = transit

	// Adapt buffer delay based on jitter
	targetDelay := jb.jitterEstimate * 2.0
	if targetDelay < jb.minDelayMs {
		targetDelay = jb.minDelayMs
	}
	if targetDelay > jb.maxDelayMs {
		targetDelay = jb.maxDelayMs
	}
	// Smooth the adaptation
	jb.currentDelayMs += 0.1 * (targetDelay - jb.currentDelayMs)

	// Detect losses: if seqNum is ahead of nextSeq by more than 1
	if seqAfter(seqNum, jb.nextSeq) {
		gap := seqDiff(seqNum, jb.nextSeq)
		if gap > 1 && gap < 1000 {
			jb.packetsLost += uint64(gap - 1)
		}
	}

	// Store packet
	pkt := &BufferedPacket{
		Data:       make([]byte, len(data)),
		SeqNum:     seqNum,
		Timestamp:  timestamp,
		ReceivedAt: now,
	}
	copy(pkt.Data, data)
	jb.packets[seqNum] = pkt

	jb.lastReceiveTime = now
	return true
}

// Pop removes and returns the next in-order packet from the buffer.
// Returns nil if the next packet hasn't arrived yet or the buffer is empty.
func (jb *JitterBuffer) Pop() *BufferedPacket {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	pkt, ok := jb.packets[jb.nextSeq]
	if !ok {
		return nil
	}

	// Check if we should wait longer (buffer delay)
	elapsed := time.Since(pkt.ReceivedAt).Milliseconds()
	if float64(elapsed) < jb.currentDelayMs {
		return nil
	}

	delete(jb.packets, jb.nextSeq)
	jb.nextSeq++
	return pkt
}

// PopForce removes and returns the next in-order packet without delay check.
// Returns nil if the next packet hasn't arrived.
func (jb *JitterBuffer) PopForce() *BufferedPacket {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	pkt, ok := jb.packets[jb.nextSeq]
	if !ok {
		return nil
	}

	delete(jb.packets, jb.nextSeq)
	jb.nextSeq++
	return pkt
}

// Stats returns current jitter buffer statistics.
func (jb *JitterBuffer) Stats() JitterBufferStats {
	jb.mu.Lock()
	defer jb.mu.Unlock()
	return JitterBufferStats{
		JitterMs:        jb.jitterEstimate,
		CurrentDelayMs:  jb.currentDelayMs,
		BufferedPackets: len(jb.packets),
		PacketsReceived: jb.packetsReceived,
		PacketsLost:     jb.packetsLost,
	}
}

// JitterBufferStats holds jitter buffer performance statistics.
type JitterBufferStats struct {
	JitterMs        float64 `json:"jitter_ms"`
	CurrentDelayMs  float64 `json:"current_delay_ms"`
	BufferedPackets int     `json:"buffered_packets"`
	PacketsReceived uint64  `json:"packets_received"`
	PacketsLost     uint64  `json:"packets_lost"`
}

// Reset clears the jitter buffer.
func (jb *JitterBuffer) Reset() {
	jb.mu.Lock()
	defer jb.mu.Unlock()
	jb.packets = make(map[uint16]*BufferedPacket, jb.capacity)
	jb.initialized = false
	jb.jitterEstimate = 0
	jb.lastTransit = 0
	jb.packetsReceived = 0
	jb.packetsLost = 0
	jb.currentDelayMs = jb.minDelayMs
}

func (jb *JitterBuffer) dropOldest() {
	var oldestSeq uint16
	var oldestTime time.Time
	found := false
	for seq, pkt := range jb.packets {
		if !found || pkt.ReceivedAt.Before(oldestTime) {
			oldestSeq = seq
			oldestTime = pkt.ReceivedAt
			found = true
		}
	}
	if found {
		delete(jb.packets, oldestSeq)
	}
}

// seqAfter returns true if a comes after b in the RTP sequence number space
// (handling wraparound).
func seqAfter(a, b uint16) bool {
	return int16(a-b) > 0
}

// seqDiff returns the positive difference between two sequence numbers.
func seqDiff(a, b uint16) int {
	diff := int16(a - b)
	if diff < 0 {
		diff = -diff
	}
	return int(diff)
}
