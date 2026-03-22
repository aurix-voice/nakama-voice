package sfu

import (
	"sync"
	"time"

	"github.com/pion/rtp"
)

// Router manages RTP packet forwarding between peers within a room.
// It handles SSRC rewriting per receiver, forwarding eligibility checks,
// and per-receiver packet queues.
type Router struct {
	roomID string

	// tracks maps sender user ID to their active track SSRC.
	tracks map[string]uint32

	// receivers maps receiver user ID to their outbound packet channel.
	receivers map[string]*ReceiverQueue

	// eligibilityFn checks whether a sender's audio should be forwarded
	// to a specific receiver. Set by the room layer.
	eligibilityFn ForwardingEligibilityFunc

	// packetPool reuses RTP packet buffers.
	packetPool *sync.Pool

	// stats
	totalForwarded uint64
	totalDropped   uint64

	// mu protects tracks, receivers, and stats.
	mu sync.RWMutex
}

// ForwardingEligibilityFunc determines whether audio from senderID should
// be forwarded to receiverID. Returns true if forwarding is allowed.
type ForwardingEligibilityFunc func(senderID, receiverID string) bool

// TaggedPacket wraps an RTP packet with the sender's user ID so the
// writer goroutine can route it to the correct send track.
type TaggedPacket struct {
	SenderID string
	Packet   *rtp.Packet
}

// ReceiverQueue is a buffered channel for outbound RTP packets to a receiver.
type ReceiverQueue struct {
	UserID   string
	Packets  chan *TaggedPacket
	SSRC     uint32 // Rewritten SSRC for this receiver
	closed   bool
	closeMu  sync.Mutex
}

// NewRouter creates a packet router for the specified room.
func NewRouter(roomID string) *Router {
	return &Router{
		roomID:    roomID,
		tracks:    make(map[string]uint32, 32),
		receivers: make(map[string]*ReceiverQueue, 32),
		packetPool: &sync.Pool{
			New: func() interface{} {
				buf := make([]byte, 1500)
				return &buf
			},
		},
		eligibilityFn: func(_, _ string) bool { return true },
	}
}

// SetEligibilityFunc sets the forwarding eligibility function.
func (r *Router) SetEligibilityFunc(fn ForwardingEligibilityFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.eligibilityFn = fn
}

// AddSender registers a sender's track SSRC.
func (r *Router) AddSender(userID string, ssrc uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tracks[userID] = ssrc
}

// RemoveSender removes a sender's track.
func (r *Router) RemoveSender(userID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tracks, userID)
}

// AddReceiver registers a receiver with a buffered packet queue.
// The rewriteSSRC is the SSRC that outbound packets will be rewritten to.
func (r *Router) AddReceiver(userID string, rewriteSSRC uint32, queueSize int) *ReceiverQueue {
	r.mu.Lock()
	defer r.mu.Unlock()

	rq := &ReceiverQueue{
		UserID:  userID,
		Packets: make(chan *TaggedPacket, queueSize),
		SSRC:    rewriteSSRC,
	}
	r.receivers[userID] = rq
	return rq
}

// RemoveReceiver removes a receiver and closes its packet queue.
func (r *Router) RemoveReceiver(userID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if rq, ok := r.receivers[userID]; ok {
		rq.closeMu.Lock()
		if !rq.closed {
			rq.closed = true
			close(rq.Packets)
		}
		rq.closeMu.Unlock()
		delete(r.receivers, userID)
	}
}

// RoutePacket forwards an RTP packet from a sender to all eligible receivers.
// It rewrites the SSRC for each receiver and sends via non-blocking channel write.
// Returns the number of receivers the packet was forwarded to.
func (r *Router) RoutePacket(senderID string, pkt *rtp.Packet) int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	forwarded := 0
	for receiverID, rq := range r.receivers {
		if receiverID == senderID {
			continue
		}

		if !r.eligibilityFn(senderID, receiverID) {
			continue
		}

		// Clone packet with rewritten SSRC
		clonedPkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        pkt.Header.Version,
				Padding:        pkt.Header.Padding,
				Extension:      pkt.Header.Extension,
				Marker:         pkt.Header.Marker,
				PayloadType:    pkt.Header.PayloadType,
				SequenceNumber: pkt.Header.SequenceNumber,
				Timestamp:      pkt.Header.Timestamp,
				SSRC:           rq.SSRC,
				CSRC:           pkt.Header.CSRC,
			},
			Payload: make([]byte, len(pkt.Payload)),
		}
		copy(clonedPkt.Payload, pkt.Payload)

		// Non-blocking send to receiver queue
		rq.closeMu.Lock()
		if !rq.closed {
			tagged := &TaggedPacket{SenderID: senderID, Packet: clonedPkt}
			select {
			case rq.Packets <- tagged:
				forwarded++
			default:
				r.totalDropped++
			}
		}
		rq.closeMu.Unlock()
	}

	r.totalForwarded += uint64(forwarded)
	return forwarded
}

// Stats returns routing statistics.
func (r *Router) Stats() RouterStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return RouterStats{
		ActiveSenders:   len(r.tracks),
		ActiveReceivers: len(r.receivers),
		TotalForwarded:  r.totalForwarded,
		TotalDropped:    r.totalDropped,
	}
}

// RouterStats holds routing statistics.
type RouterStats struct {
	ActiveSenders   int    `json:"active_senders"`
	ActiveReceivers int    `json:"active_receivers"`
	TotalForwarded  uint64 `json:"total_forwarded"`
	TotalDropped    uint64 `json:"total_dropped"`
}

// DrainQueue reads all available packets from a receiver queue with a timeout.
// Returns the packets read. Used primarily for testing.
func DrainQueue(rq *ReceiverQueue, timeout time.Duration) []*TaggedPacket {
	var packets []*TaggedPacket
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case pkt, ok := <-rq.Packets:
			if !ok {
				return packets
			}
			packets = append(packets, pkt)
		case <-timer.C:
			return packets
		}
	}
}