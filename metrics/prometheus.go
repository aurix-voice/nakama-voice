// Package metrics provides Prometheus metric registrations and update helpers
// for the voice chat module.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// VoiceMetrics holds all Prometheus metric variables for the voice module.
type VoiceMetrics struct {
	// ActiveRooms tracks the current number of active voice rooms, labeled by type.
	ActiveRooms *prometheus.GaugeVec
	// ActiveParticipants tracks participants per room type.
	ActiveParticipants *prometheus.GaugeVec
	// PeerConnectionStates counts peer connection state transitions.
	PeerConnectionStates *prometheus.CounterVec
	// RTPPacketsForwarded counts RTP packets forwarded per room.
	RTPPacketsForwarded *prometheus.CounterVec
	// RTPBytesForwarded counts RTP bytes forwarded per room.
	RTPBytesForwarded *prometheus.CounterVec
	// RTPPacketLoss tracks packet loss rate per room.
	RTPPacketLoss *prometheus.GaugeVec
	// JitterBufferDelay is a histogram of jitter buffer delay in milliseconds.
	JitterBufferDelay prometheus.Histogram
	// MOSScore is a histogram of estimated Mean Opinion Score values.
	MOSScore prometheus.Histogram
	// TURNAllocations tracks current TURN allocation count.
	TURNAllocations prometheus.Gauge
	// TURNBytesRelayed counts bytes relayed through TURN.
	TURNBytesRelayed prometheus.Counter
	// SignalingMessages counts signaling messages by opcode.
	SignalingMessages *prometheus.CounterVec
	// SignalingErrors counts signaling errors by opcode.
	SignalingErrors *prometheus.CounterVec
	// RoomJoinLatency is a histogram of room join latency in milliseconds.
	RoomJoinLatency prometheus.Histogram
	// ActiveRecordings tracks active recording sessions.
	ActiveRecordings prometheus.Gauge
	// ModerationActions counts moderation actions by type.
	ModerationActions *prometheus.CounterVec
	// ClusterNodes tracks active cluster nodes.
	ClusterNodes prometheus.Gauge
	// ClusterSyncLatency is a histogram of cluster sync latency in milliseconds.
	ClusterSyncLatency prometheus.Histogram
}

// NewVoiceMetrics creates and registers all Prometheus metrics.
// Returns the metrics struct. Panics if registration fails (startup only).
func NewVoiceMetrics() *VoiceMetrics {
	m := &VoiceMetrics{
		ActiveRooms: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "nakama_voice",
			Name:      "active_rooms",
			Help:      "Number of active voice rooms by type.",
		}, []string{"type"}),
		ActiveParticipants: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "nakama_voice",
			Name:      "active_participants",
			Help:      "Number of active participants by room type.",
		}, []string{"type"}),
		PeerConnectionStates: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nakama_voice",
			Name:      "peer_connection_state_transitions_total",
			Help:      "Peer connection state transitions.",
		}, []string{"state"}),
		RTPPacketsForwarded: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nakama_voice",
			Name:      "rtp_packets_forwarded_total",
			Help:      "RTP packets forwarded per room.",
		}, []string{"room_id"}),
		RTPBytesForwarded: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nakama_voice",
			Name:      "rtp_bytes_forwarded_total",
			Help:      "RTP bytes forwarded per room.",
		}, []string{"room_id"}),
		RTPPacketLoss: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "nakama_voice",
			Name:      "rtp_packet_loss_rate",
			Help:      "Current RTP packet loss rate per room.",
		}, []string{"room_id"}),
		JitterBufferDelay: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "nakama_voice",
			Name:      "jitter_buffer_delay_ms",
			Help:      "Jitter buffer delay in milliseconds.",
			Buckets:   []float64{5, 10, 20, 40, 60, 80, 100, 150, 200},
		}),
		MOSScore: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "nakama_voice",
			Name:      "mos_score",
			Help:      "Estimated Mean Opinion Score.",
			Buckets:   []float64{1.0, 1.5, 2.0, 2.5, 3.0, 3.5, 4.0, 4.5, 5.0},
		}),
		TURNAllocations: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "nakama_voice",
			Name:      "turn_allocations",
			Help:      "Current TURN allocation count.",
		}),
		TURNBytesRelayed: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nakama_voice",
			Name:      "turn_bytes_relayed_total",
			Help:      "Total bytes relayed through TURN.",
		}),
		SignalingMessages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nakama_voice",
			Name:      "signaling_messages_total",
			Help:      "Signaling messages by opcode.",
		}, []string{"opcode"}),
		SignalingErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nakama_voice",
			Name:      "signaling_errors_total",
			Help:      "Signaling errors by opcode.",
		}, []string{"opcode"}),
		RoomJoinLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "nakama_voice",
			Name:      "room_join_latency_ms",
			Help:      "Room join latency in milliseconds.",
			Buckets:   []float64{10, 25, 50, 100, 200, 500, 1000},
		}),
		ActiveRecordings: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "nakama_voice",
			Name:      "active_recordings",
			Help:      "Number of active recording sessions.",
		}),
		ModerationActions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nakama_voice",
			Name:      "moderation_actions_total",
			Help:      "Moderation actions by type.",
		}, []string{"action"}),
		ClusterNodes: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "nakama_voice",
			Name:      "cluster_nodes",
			Help:      "Active cluster node count.",
		}),
		ClusterSyncLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "nakama_voice",
			Name:      "cluster_sync_latency_ms",
			Help:      "Cluster synchronization latency in milliseconds.",
			Buckets:   []float64{1, 5, 10, 25, 50, 100},
		}),
	}

	prometheus.MustRegister(
		m.ActiveRooms,
		m.ActiveParticipants,
		m.PeerConnectionStates,
		m.RTPPacketsForwarded,
		m.RTPBytesForwarded,
		m.RTPPacketLoss,
		m.JitterBufferDelay,
		m.MOSScore,
		m.TURNAllocations,
		m.TURNBytesRelayed,
		m.SignalingMessages,
		m.SignalingErrors,
		m.RoomJoinLatency,
		m.ActiveRecordings,
		m.ModerationActions,
		m.ClusterNodes,
		m.ClusterSyncLatency,
	)

	return m
}

// IncRoomJoined increments the active rooms and participants gauges.
func (m *VoiceMetrics) IncRoomJoined(roomType string) {
	m.ActiveParticipants.WithLabelValues(roomType).Inc()
}

// DecRoomLeft decrements the active participants gauge.
func (m *VoiceMetrics) DecRoomLeft(roomType string) {
	m.ActiveParticipants.WithLabelValues(roomType).Dec()
}

// SetActiveRooms sets the active rooms gauge for a type.
func (m *VoiceMetrics) SetActiveRooms(roomType string, count float64) {
	m.ActiveRooms.WithLabelValues(roomType).Set(count)
}

// IncSignalingMessage increments the signaling message counter for an opcode.
func (m *VoiceMetrics) IncSignalingMessage(opcode string) {
	m.SignalingMessages.WithLabelValues(opcode).Inc()
}

// IncSignalingError increments the signaling error counter for an opcode.
func (m *VoiceMetrics) IncSignalingError(opcode string) {
	m.SignalingErrors.WithLabelValues(opcode).Inc()
}

// IncModerationAction increments the moderation action counter.
func (m *VoiceMetrics) IncModerationAction(action string) {
	m.ModerationActions.WithLabelValues(action).Inc()
}

// ObserveRoomJoinLatency records a room join latency value in milliseconds.
func (m *VoiceMetrics) ObserveRoomJoinLatency(ms float64) {
	m.RoomJoinLatency.Observe(ms)
}

// IncRTPForwarded increments RTP packet and byte counters for a room.
func (m *VoiceMetrics) IncRTPForwarded(roomID string, bytes int) {
	m.RTPPacketsForwarded.WithLabelValues(roomID).Inc()
	m.RTPBytesForwarded.WithLabelValues(roomID).Add(float64(bytes))
}

// ObserveJitterDelay records a jitter buffer delay observation.
func (m *VoiceMetrics) ObserveJitterDelay(ms float64) {
	m.JitterBufferDelay.Observe(ms)
}

// ObserveMOS records a MOS score observation.
func (m *VoiceMetrics) ObserveMOS(score float64) {
	m.MOSScore.Observe(score)
}
