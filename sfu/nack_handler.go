package sfu

import (
	"sync"
	"time"

	"github.com/pion/rtcp"
)

// NACKHandler detects gaps in received RTP sequence numbers and generates
// NACK (Negative Acknowledgement) packets requesting retransmission.
// It also manages a retransmission queue of recently sent packets.
type NACKHandler struct {
	// maxRetxSize is the maximum number of packets to keep for retransmission.
	maxRetxSize int
	// maxNACKAge is how long to wait for a missing packet before giving up.
	maxNACKAge time.Duration
	// retxBuffer holds recently sent packets for retransmission, keyed by seq num.
	retxBuffer map[uint16]*retxPacket
	// pendingNACKs tracks sequence numbers we've sent NACKs for but haven't received.
	pendingNACKs map[uint16]*nackEntry
	// highestSeq is the highest sequence number we've received.
	highestSeq uint16
	// ssrc is the SSRC of the stream being tracked.
	ssrc uint32
	// initialized tracks whether we've received the first packet.
	initialized bool

	// Loss statistics
	totalExpected uint64
	totalReceived uint64
	totalRetx     uint64

	// mu protects all mutable state.
	mu sync.Mutex
}

type retxPacket struct {
	data      []byte
	seq       uint16
	timestamp time.Time
}

type nackEntry struct {
	seq       uint16
	firstNACK time.Time
	attempts  int
}

// NewNACKHandler creates a NACK handler for the specified SSRC with the given
// retransmission buffer size and maximum NACK age.
func NewNACKHandler(ssrc uint32, maxRetxSize int, maxNACKAge time.Duration) *NACKHandler {
	return &NACKHandler{
		maxRetxSize:  maxRetxSize,
		maxNACKAge:   maxNACKAge,
		retxBuffer:   make(map[uint16]*retxPacket, maxRetxSize),
		pendingNACKs: make(map[uint16]*nackEntry, 64),
		ssrc:         ssrc,
	}
}

// RecordSent records a sent packet for potential retransmission.
func (nh *NACKHandler) RecordSent(data []byte, seqNum uint16) {
	nh.mu.Lock()
	defer nh.mu.Unlock()

	pktCopy := make([]byte, len(data))
	copy(pktCopy, data)

	nh.retxBuffer[seqNum] = &retxPacket{
		data:      pktCopy,
		seq:       seqNum,
		timestamp: time.Now(),
	}

	// Evict oldest if over capacity
	if len(nh.retxBuffer) > nh.maxRetxSize {
		nh.evictOldest()
	}
}

// GetRetransmission returns the data for a previously sent packet with
// the given sequence number. Returns nil if the packet is not available.
func (nh *NACKHandler) GetRetransmission(seqNum uint16) []byte {
	nh.mu.Lock()
	defer nh.mu.Unlock()

	pkt, ok := nh.retxBuffer[seqNum]
	if !ok {
		return nil
	}
	nh.totalRetx++
	return pkt.data
}

// RecordReceived records that a packet with the given sequence number was
// received. Returns a list of sequence numbers that should be NACKed (gaps detected).
func (nh *NACKHandler) RecordReceived(seqNum uint16) []uint16 {
	nh.mu.Lock()
	defer nh.mu.Unlock()

	nh.totalReceived++

	// Remove from pending NACKs if present
	delete(nh.pendingNACKs, seqNum)

	if !nh.initialized {
		nh.highestSeq = seqNum
		nh.initialized = true
		return nil
	}

	var missingSeqs []uint16

	if seqAfter(seqNum, nh.highestSeq) {
		// Detect gaps
		gap := seqDiff(seqNum, nh.highestSeq)
		if gap > 1 && gap < 100 {
			nh.totalExpected += uint64(gap)
			for i := nh.highestSeq + 1; i != seqNum; i++ {
				// Only NACK if not already pending
				if _, already := nh.pendingNACKs[i]; !already {
					nh.pendingNACKs[i] = &nackEntry{
						seq:       i,
						firstNACK: time.Now(),
						attempts:  0,
					}
					missingSeqs = append(missingSeqs, i)
				}
			}
		}
		nh.highestSeq = seqNum
	}

	return missingSeqs
}

// BuildNACKPacket creates an RTCP NACK packet for the given missing sequence numbers.
// Returns nil if no sequences need NACKing.
func (nh *NACKHandler) BuildNACKPacket(senderSSRC uint32, missingSeqs []uint16) *rtcp.TransportLayerNack {
	if len(missingSeqs) == 0 {
		return nil
	}

	pairs := make([]rtcp.NackPair, 0, len(missingSeqs))
	for _, seq := range missingSeqs {
		pairs = append(pairs, rtcp.NackPair{
			PacketID:    seq,
			LostPackets: 0, // Single packet loss per pair for simplicity
		})
	}

	return &rtcp.TransportLayerNack{
		SenderSSRC: senderSSRC,
		MediaSSRC:  nh.ssrc,
		Nacks:      pairs,
	}
}

// CleanupExpired removes pending NACKs that have exceeded the maximum age.
// Returns the count of expired entries.
func (nh *NACKHandler) CleanupExpired() int {
	nh.mu.Lock()
	defer nh.mu.Unlock()

	expired := 0
	now := time.Now()
	for seq, entry := range nh.pendingNACKs {
		if now.Sub(entry.firstNACK) > nh.maxNACKAge {
			delete(nh.pendingNACKs, seq)
			expired++
		}
	}

	// Also clean old retx buffer entries
	for seq, pkt := range nh.retxBuffer {
		if now.Sub(pkt.timestamp) > nh.maxNACKAge*2 {
			delete(nh.retxBuffer, seq)
		}
	}

	return expired
}

// LossRate returns the current packet loss rate (0.0 to 1.0).
func (nh *NACKHandler) LossRate() float64 {
	nh.mu.Lock()
	defer nh.mu.Unlock()

	if nh.totalExpected == 0 {
		return 0
	}
	lost := nh.totalExpected - nh.totalReceived
	return float64(lost) / float64(nh.totalExpected)
}

// Stats returns NACK handler statistics.
func (nh *NACKHandler) Stats() NACKStats {
	nh.mu.Lock()
	defer nh.mu.Unlock()
	return NACKStats{
		TotalReceived:   nh.totalReceived,
		TotalExpected:   nh.totalExpected,
		TotalRetransmit: nh.totalRetx,
		PendingNACKs:    len(nh.pendingNACKs),
		RetxBufferSize:  len(nh.retxBuffer),
	}
}

// NACKStats holds NACK handler statistics.
type NACKStats struct {
	TotalReceived   uint64 `json:"total_received"`
	TotalExpected   uint64 `json:"total_expected"`
	TotalRetransmit uint64 `json:"total_retransmit"`
	PendingNACKs    int    `json:"pending_nacks"`
	RetxBufferSize  int    `json:"retx_buffer_size"`
}

func (nh *NACKHandler) evictOldest() {
	var oldestSeq uint16
	var oldestTime time.Time
	found := false
	for seq, pkt := range nh.retxBuffer {
		if !found || pkt.timestamp.Before(oldestTime) {
			oldestSeq = seq
			oldestTime = pkt.timestamp
			found = true
		}
	}
	if found {
		delete(nh.retxBuffer, oldestSeq)
	}
}
