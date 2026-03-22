// Package cluster implements multi-node coordination via Redis for the
// voice chat module, including consistent hashing, node heartbeats,
// and room migration on node failure.
package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/aurix-voice/nakama-voice/config"
	"github.com/aurix-voice/nakama-voice/metrics"
)

// Coordinator manages cluster-wide room assignment using consistent hashing,
// node heartbeats for failure detection, and room migration.
type Coordinator struct {
	nodeID       string
	rdb          *redis.Client
	cfg          config.ClusterConfig
	voiceMetrics *metrics.VoiceMetrics

	// ring is the consistent hashing ring of active nodes.
	ring *HashRing

	// onRoomMigrate is called when a room needs to migrate to this node.
	onRoomMigrate func(roomID string)

	ctx    context.Context
	cancel context.CancelFunc

	// enabled tracks whether Redis is available.
	enabled bool

	// mu protects ring and enabled.
	mu sync.RWMutex
}

// NewCoordinator creates a cluster coordinator. If the Redis address is empty,
// the coordinator operates in single-node mode.
func NewCoordinator(parentCtx context.Context, cfg config.ClusterConfig, m *metrics.VoiceMetrics) *Coordinator {
	ctx, cancel := context.WithCancel(parentCtx)
	nodeID := uuid.New().String()

	c := &Coordinator{
		nodeID:       nodeID,
		cfg:          cfg,
		voiceMetrics: m,
		ring:         NewHashRing(100),
		ctx:          ctx,
		cancel:       cancel,
	}

	if cfg.RedisAddress != "" {
		c.rdb = redis.NewClient(&redis.Options{
			Addr:         cfg.RedisAddress,
			Password:     cfg.RedisPassword,
			DB:           cfg.RedisDB,
			MinIdleConns: cfg.PoolMinConns,
			PoolSize:     cfg.PoolMaxConns,
		})

		// Test connection
		pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
		defer pingCancel()
		if err := c.rdb.Ping(pingCtx).Err(); err != nil {
			c.enabled = false
		} else {
			c.enabled = true
			c.ring.Add(nodeID)
		}
	}

	return c
}

// Start begins heartbeat emission and node monitoring.
func (c *Coordinator) Start() {
	if !c.enabled {
		return
	}

	go c.heartbeatLoop()
	go c.monitorLoop()
}

// IsEnabled returns whether cluster coordination is active.
func (c *Coordinator) IsEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.enabled
}

// NodeID returns the unique identifier for this node.
func (c *Coordinator) NodeID() string {
	return c.nodeID
}

// AssignRoom determines which node should own a room based on consistent hashing.
// Returns the owning node ID.
func (c *Coordinator) AssignRoom(roomID string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.enabled {
		return c.nodeID
	}

	return c.ring.Get(roomID)
}

// IsLocalRoom returns true if this node owns the specified room.
func (c *Coordinator) IsLocalRoom(roomID string) bool {
	return c.AssignRoom(roomID) == c.nodeID
}

// SetOnRoomMigrate sets the callback for room migration events.
func (c *Coordinator) SetOnRoomMigrate(fn func(roomID string)) {
	c.onRoomMigrate = fn
}

// RegisterRoom registers a room in the cluster-wide registry.
func (c *Coordinator) RegisterRoom(roomID string) error {
	if !c.enabled {
		return nil
	}

	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()

	key := fmt.Sprintf("voice:room:%s", roomID)
	return c.rdb.Set(ctx, key, c.nodeID, 5*time.Minute).Err()
}

// UnregisterRoom removes a room from the cluster-wide registry.
func (c *Coordinator) UnregisterRoom(roomID string) error {
	if !c.enabled {
		return nil
	}

	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()

	key := fmt.Sprintf("voice:room:%s", roomID)
	return c.rdb.Del(ctx, key).Err()
}

// GetNodeForRoom looks up which node currently owns a room.
func (c *Coordinator) GetNodeForRoom(roomID string) (string, error) {
	if !c.enabled {
		return c.nodeID, nil
	}

	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()

	key := fmt.Sprintf("voice:room:%s", roomID)
	nodeID, err := c.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", fmt.Errorf("cluster: room %s not registered", roomID)
	}
	if err != nil {
		return "", fmt.Errorf("cluster: get room node: %w", err)
	}
	return nodeID, nil
}

// ActiveNodes returns the set of active node IDs.
func (c *Coordinator) ActiveNodes() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ring.Nodes()
}

// Close shuts down the coordinator and Redis connection.
func (c *Coordinator) Close() error {
	c.cancel()
	if c.rdb != nil {
		return c.rdb.Close()
	}
	return nil
}

// RedisClient returns the underlying Redis client, or nil if not connected.
func (c *Coordinator) RedisClient() *redis.Client {
	return c.rdb
}

// HealthCheck returns nil if Redis is reachable.
func (c *Coordinator) HealthCheck() error {
	if !c.enabled {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return c.rdb.Ping(ctx).Err()
}

func (c *Coordinator) heartbeatLoop() {
	defer func() {
		if r := recover(); r != nil {
			// Recovered panic in heartbeat loop
		}
	}()

	interval := time.Duration(c.cfg.HeartbeatIntervalS) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.emitHeartbeat()
		}
	}
}

func (c *Coordinator) emitHeartbeat() {
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
	defer cancel()

	key := fmt.Sprintf("voice:node:%s", c.nodeID)
	ttl := time.Duration(c.cfg.HeartbeatIntervalS*3) * time.Second
	if err := c.rdb.Set(ctx, key, "alive", ttl).Err(); err != nil {
		// Heartbeat failed; may lose cluster membership
		c.mu.Lock()
		c.enabled = false
		c.mu.Unlock()
	}
}

func (c *Coordinator) monitorLoop() {
	defer func() {
		if r := recover(); r != nil {
			// Recovered panic in monitor loop
		}
	}()

	interval := time.Duration(c.cfg.HeartbeatIntervalS*2) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.checkNodes()
		}
	}
}

func (c *Coordinator) checkNodes() {
	if !c.enabled {
		return
	}

	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()

	// Scan for all node keys
	iter := c.rdb.Scan(ctx, 0, "voice:node:*", 100).Iterator()
	activeNodes := make(map[string]bool)
	for iter.Next(ctx) {
		key := iter.Val()
		nodeID := key[len("voice:node:"):]
		activeNodes[nodeID] = true
	}

	c.mu.Lock()
	// Rebuild ring with active nodes
	c.ring = NewHashRing(100)
	for nodeID := range activeNodes {
		c.ring.Add(nodeID)
	}
	if c.voiceMetrics != nil {
		c.voiceMetrics.ClusterNodes.Set(float64(len(activeNodes)))
	}
	c.mu.Unlock()
}

// HashRing implements consistent hashing for room-to-node assignment.
type HashRing struct {
	replicas int
	keys     []uint32
	hashMap  map[uint32]string
	nodes    map[string]bool
	mu       sync.RWMutex
}

// NewHashRing creates a consistent hashing ring with the specified number
// of virtual nodes per physical node.
func NewHashRing(replicas int) *HashRing {
	return &HashRing{
		replicas: replicas,
		hashMap:  make(map[uint32]string),
		nodes:    make(map[string]bool),
	}
}

// Add adds a node to the ring.
func (hr *HashRing) Add(node string) {
	hr.mu.Lock()
	defer hr.mu.Unlock()

	hr.nodes[node] = true
	for i := 0; i < hr.replicas; i++ {
		key := fmt.Sprintf("%s:%d", node, i)
		h := hashKey(key)
		hr.keys = append(hr.keys, h)
		hr.hashMap[h] = node
	}
	sort.Slice(hr.keys, func(i, j int) bool { return hr.keys[i] < hr.keys[j] })
}

// Remove removes a node from the ring.
func (hr *HashRing) Remove(node string) {
	hr.mu.Lock()
	defer hr.mu.Unlock()

	delete(hr.nodes, node)
	newKeys := make([]uint32, 0, len(hr.keys))
	for _, k := range hr.keys {
		if hr.hashMap[k] != node {
			newKeys = append(newKeys, k)
		} else {
			delete(hr.hashMap, k)
		}
	}
	hr.keys = newKeys
}

// Get returns the node responsible for the given key.
func (hr *HashRing) Get(key string) string {
	hr.mu.RLock()
	defer hr.mu.RUnlock()

	if len(hr.keys) == 0 {
		return ""
	}

	h := hashKey(key)
	idx := sort.Search(len(hr.keys), func(i int) bool { return hr.keys[i] >= h })
	if idx >= len(hr.keys) {
		idx = 0
	}
	return hr.hashMap[hr.keys[idx]]
}

// Nodes returns all registered node IDs.
func (hr *HashRing) Nodes() []string {
	hr.mu.RLock()
	defer hr.mu.RUnlock()
	nodes := make([]string, 0, len(hr.nodes))
	for n := range hr.nodes {
		nodes = append(nodes, n)
	}
	return nodes
}

func hashKey(key string) uint32 {
	h := sha256.Sum256([]byte(key))
	return binary.BigEndian.Uint32(h[:4])
}
