package room

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/your-org/nakama-voice/config"
	"github.com/your-org/nakama-voice/metrics"
)

// Manager is the room registry that handles thread-safe room creation,
// lookup, destruction, and background garbage collection of empty rooms.
type Manager struct {
	rooms    map[string]*Room
	cfg      *config.VoiceConfig
	m        *metrics.VoiceMetrics
	ctx      context.Context
	cancel   context.CancelFunc

	// mu protects the rooms map.
	mu sync.RWMutex
}

// NewManager creates a room manager and starts background GC.
func NewManager(parentCtx context.Context, cfg *config.VoiceConfig, m *metrics.VoiceMetrics) *Manager {
	ctx, cancel := context.WithCancel(parentCtx)
	mgr := &Manager{
		rooms:  make(map[string]*Room, 64),
		cfg:    cfg,
		m:      m,
		ctx:    ctx,
		cancel: cancel,
	}
	go mgr.gcLoop()
	return mgr
}

// CreateRoom creates a new voice room. Returns an error if a room with
// the same ID already exists.
func (mgr *Manager) CreateRoom(id, matchID, label, ownerID string) (*Room, error) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	if _, exists := mgr.rooms[id]; exists {
		return nil, fmt.Errorf("room manager: room %s already exists", id)
	}

	roomCfg := mgr.cfg.GetRoom()
	audioCfg := mgr.cfg.GetAudio()
	room := NewRoom(id, matchID, label, ownerID, roomCfg, audioCfg)
	mgr.rooms[id] = room

	if mgr.m != nil {
		mgr.m.SetActiveRooms("all", float64(len(mgr.rooms)))
	}

	return room, nil
}

// GetRoom returns a room by ID.
func (mgr *Manager) GetRoom(id string) *Room {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.rooms[id]
}

// DestroyRoom removes a room from the registry.
func (mgr *Manager) DestroyRoom(id string) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	delete(mgr.rooms, id)

	if mgr.m != nil {
		mgr.m.SetActiveRooms("all", float64(len(mgr.rooms)))
	}
}

// ListRooms returns metadata for all rooms, optionally limited.
func (mgr *Manager) ListRooms(limit int) []*Room {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	rooms := make([]*Room, 0, len(mgr.rooms))
	for _, r := range mgr.rooms {
		rooms = append(rooms, r)
		if limit > 0 && len(rooms) >= limit {
			break
		}
	}
	return rooms
}

// RoomCount returns the number of active rooms.
func (mgr *Manager) RoomCount() int {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return len(mgr.rooms)
}

// GetAllRoomStats implements metrics.StatsProvider for custom Prometheus collectors.
func (mgr *Manager) GetAllRoomStats() map[string]*metrics.RoomStats {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	stats := make(map[string]*metrics.RoomStats, len(mgr.rooms))
	for id := range mgr.rooms {
		stats[id] = &metrics.RoomStats{
			PacketsSent: 0,
			PacketsLost: 0,
			JitterMs:    0,
		}
	}
	return stats
}

// Close shuts down the room manager and stops GC.
func (mgr *Manager) Close() {
	mgr.cancel()
}

// gcLoop runs every 30 seconds to remove empty rooms.
func (mgr *Manager) gcLoop() {
	defer func() {
		if r := recover(); r != nil {
			// Recovered panic in room GC loop
		}
	}()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-mgr.ctx.Done():
			return
		case <-ticker.C:
			mgr.collectEmpty()
		}
	}
}

func (mgr *Manager) collectEmpty() {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	for id, room := range mgr.rooms {
		if room.IsEmpty() && time.Since(room.CreatedAt) > 60*time.Second {
			delete(mgr.rooms, id)
		}
	}

	if mgr.m != nil {
		mgr.m.SetActiveRooms("all", float64(len(mgr.rooms)))
	}
}