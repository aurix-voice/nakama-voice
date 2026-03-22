package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// StateSync manages distributed room state synchronisation via Redis pub/sub.
// Local state changes are published to a Redis channel, and remote changes
// are applied to the local room manager.
type StateSync struct {
	rdb      *redis.Client
	nodeID   string
	channel  string

	// onRemoteEvent is called when a state change from another node is received.
	onRemoteEvent func(event StateEvent)

	pubsub *redis.PubSub
	ctx    context.Context
	cancel context.CancelFunc

	// mu protects onRemoteEvent.
	mu sync.RWMutex
}

// StateEvent represents a state change that needs to be synchronised
// across the cluster.
type StateEvent struct {
	Type     string `json:"type"`
	NodeID   string `json:"node_id"`
	RoomID   string `json:"room_id"`
	UserID   string `json:"user_id,omitempty"`
	Data     string `json:"data,omitempty"`
	Timestamp int64 `json:"timestamp"`
}

// State event types.
const (
	EventRoomCreated    = "room_created"
	EventRoomDestroyed  = "room_destroyed"
	EventParticipantJoin = "participant_join"
	EventParticipantLeave = "participant_leave"
	EventMuteChange     = "mute_change"
	EventBanAdded       = "ban_added"
)

// NewStateSync creates a state synchroniser using Redis pub/sub.
// Returns nil if rdb is nil (single-node mode).
func NewStateSync(parentCtx context.Context, rdb *redis.Client, nodeID string) *StateSync {
	if rdb == nil {
		return nil
	}

	ctx, cancel := context.WithCancel(parentCtx)
	return &StateSync{
		rdb:     rdb,
		nodeID:  nodeID,
		channel: "voice:state",
		ctx:     ctx,
		cancel:  cancel,
	}
}

// SetOnRemoteEvent sets the handler for events received from other nodes.
func (ss *StateSync) SetOnRemoteEvent(fn func(event StateEvent)) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.onRemoteEvent = fn
}

// Start begins subscribing to state change events.
func (ss *StateSync) Start() error {
	ss.pubsub = ss.rdb.Subscribe(ss.ctx, ss.channel)

	// Wait for subscription confirmation
	_, err := ss.pubsub.Receive(ss.ctx)
	if err != nil {
		return fmt.Errorf("cluster state: subscribe: %w", err)
	}

	go ss.receiveLoop()
	return nil
}

// Publish sends a state change event to all other nodes.
func (ss *StateSync) Publish(event StateEvent) error {
	event.NodeID = ss.nodeID
	event.Timestamp = time.Now().UnixMilli()

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("cluster state: marshal event: %w", err)
	}

	ctx, cancel := context.WithTimeout(ss.ctx, 3*time.Second)
	defer cancel()

	return ss.rdb.Publish(ctx, ss.channel, data).Err()
}

// Close shuts down the state synchroniser.
func (ss *StateSync) Close() error {
	ss.cancel()
	if ss.pubsub != nil {
		return ss.pubsub.Close()
	}
	return nil
}

func (ss *StateSync) receiveLoop() {
	defer func() {
		if r := recover(); r != nil {
			// Recovered panic in state sync receive loop
		}
	}()

	ch := ss.pubsub.Channel()
	for {
		select {
		case <-ss.ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}

			var event StateEvent
			if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
				continue
			}

			// Ignore events from this node
			if event.NodeID == ss.nodeID {
				continue
			}

			ss.mu.RLock()
			handler := ss.onRemoteEvent
			ss.mu.RUnlock()

			if handler != nil {
				handler(event)
			}
		}
	}
}
