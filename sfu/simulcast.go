package sfu

import (
	"sync"
)

// SimulcastLayer represents a single quality layer in a simulcast stream.
type SimulcastLayer int

const (
	// SimulcastLow is the lowest quality layer.
	SimulcastLow SimulcastLayer = 0
	// SimulcastMid is the middle quality layer.
	SimulcastMid SimulcastLayer = 1
	// SimulcastHigh is the highest quality layer.
	SimulcastHigh SimulcastLayer = 2
)

// SimulcastTrack tracks the available simulcast layers from a single sender
// and allows dynamic layer selection per receiver based on bandwidth.
type SimulcastTrack struct {
	senderID string
	// layers maps layer to the SSRC and bitrate of that layer.
	layers map[SimulcastLayer]*LayerInfo

	// mu protects the layers map.
	mu sync.RWMutex
}

// LayerInfo holds information about a single simulcast layer.
type LayerInfo struct {
	SSRC       uint32  `json:"ssrc"`
	BitrateKbps float64 `json:"bitrate_kbps"`
	Active     bool    `json:"active"`
}

// NewSimulcastTrack creates a new simulcast track for the specified sender.
func NewSimulcastTrack(senderID string) *SimulcastTrack {
	return &SimulcastTrack{
		senderID: senderID,
		layers:   make(map[SimulcastLayer]*LayerInfo, 3),
	}
}

// AddLayer registers a simulcast layer with the given SSRC and bitrate.
func (st *SimulcastTrack) AddLayer(layer SimulcastLayer, ssrc uint32, bitrateKbps float64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.layers[layer] = &LayerInfo{
		SSRC:        ssrc,
		BitrateKbps: bitrateKbps,
		Active:      true,
	}
}

// RemoveLayer removes a simulcast layer.
func (st *SimulcastTrack) RemoveLayer(layer SimulcastLayer) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.layers, layer)
}

// SetLayerActive marks a layer as active or inactive.
func (st *SimulcastTrack) SetLayerActive(layer SimulcastLayer, active bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if info, ok := st.layers[layer]; ok {
		info.Active = active
	}
}

// GetBestLayer selects the highest quality active layer that fits within
// the specified available bandwidth in kbps. Returns the layer and its SSRC.
// If no layer fits, returns the lowest available layer.
func (st *SimulcastTrack) GetBestLayer(availableBandwidthKbps float64) (SimulcastLayer, uint32) {
	st.mu.RLock()
	defer st.mu.RUnlock()

	// Try from highest to lowest
	for _, layer := range []SimulcastLayer{SimulcastHigh, SimulcastMid, SimulcastLow} {
		info, ok := st.layers[layer]
		if !ok || !info.Active {
			continue
		}
		if info.BitrateKbps <= availableBandwidthKbps {
			return layer, info.SSRC
		}
	}

	// Fallback: return lowest available layer regardless of bandwidth
	for _, layer := range []SimulcastLayer{SimulcastLow, SimulcastMid, SimulcastHigh} {
		info, ok := st.layers[layer]
		if ok && info.Active {
			return layer, info.SSRC
		}
	}

	return SimulcastLow, 0
}

// GetLayerSSRC returns the SSRC for a specific layer, or 0 if not available.
func (st *SimulcastTrack) GetLayerSSRC(layer SimulcastLayer) uint32 {
	st.mu.RLock()
	defer st.mu.RUnlock()
	if info, ok := st.layers[layer]; ok {
		return info.SSRC
	}
	return 0
}

// AvailableLayers returns all available active layers.
func (st *SimulcastTrack) AvailableLayers() []SimulcastLayer {
	st.mu.RLock()
	defer st.mu.RUnlock()
	layers := make([]SimulcastLayer, 0, len(st.layers))
	for l, info := range st.layers {
		if info.Active {
			layers = append(layers, l)
		}
	}
	return layers
}

// SimulcastManager manages simulcast track selection across all receivers
// in a room.
type SimulcastManager struct {
	// tracks maps sender user ID to their simulcast track info.
	tracks map[string]*SimulcastTrack
	// receiverSelections maps receiver user ID → sender user ID → selected layer.
	receiverSelections map[string]map[string]SimulcastLayer

	// mu protects tracks and receiverSelections.
	mu sync.RWMutex
}

// NewSimulcastManager creates a simulcast manager for a room.
func NewSimulcastManager() *SimulcastManager {
	return &SimulcastManager{
		tracks:             make(map[string]*SimulcastTrack, 16),
		receiverSelections: make(map[string]map[string]SimulcastLayer, 16),
	}
}

// RegisterSender registers a sender's simulcast track.
func (sm *SimulcastManager) RegisterSender(senderID string, track *SimulcastTrack) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.tracks[senderID] = track
}

// UnregisterSender removes a sender's simulcast track.
func (sm *SimulcastManager) UnregisterSender(senderID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.tracks, senderID)
	for _, selections := range sm.receiverSelections {
		delete(selections, senderID)
	}
}

// SelectLayerForReceiver selects the optimal simulcast layer for a receiver
// from a specific sender based on the receiver's available bandwidth.
// Returns the selected layer and the SSRC to forward.
func (sm *SimulcastManager) SelectLayerForReceiver(receiverID, senderID string, bandwidthKbps float64) (SimulcastLayer, uint32) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	track, ok := sm.tracks[senderID]
	if !ok {
		return SimulcastLow, 0
	}

	layer, ssrc := track.GetBestLayer(bandwidthKbps)

	if _, ok := sm.receiverSelections[receiverID]; !ok {
		sm.receiverSelections[receiverID] = make(map[string]SimulcastLayer, 8)
	}
	sm.receiverSelections[receiverID][senderID] = layer

	return layer, ssrc
}

// GetCurrentSelection returns the currently selected layer for a receiver
// from a specific sender.
func (sm *SimulcastManager) GetCurrentSelection(receiverID, senderID string) SimulcastLayer {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if selections, ok := sm.receiverSelections[receiverID]; ok {
		if layer, ok := selections[senderID]; ok {
			return layer
		}
	}
	return SimulcastLow
}

// RemoveReceiver clears all layer selections for a receiver.
func (sm *SimulcastManager) RemoveReceiver(receiverID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.receiverSelections, receiverID)
}
