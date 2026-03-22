package cluster

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pion/rtp"
)

// Bridge manages cross-node UDP audio tunnels for forwarding RTP packets
// between SFU nodes when participants in the same room are on different nodes.
type Bridge struct {
	localAddr  string
	localConn  *net.UDPConn

	// tunnels maps "remoteNodeID:roomID" to the UDP tunnel for that pair.
	tunnels map[string]*Tunnel

	// onPacketReceived is called when an RTP packet arrives from a remote node.
	onPacketReceived func(roomID, userID string, pkt *rtp.Packet)

	ctx    context.Context
	cancel context.CancelFunc

	// mu protects tunnels.
	mu sync.RWMutex
}

// Tunnel represents a UDP tunnel to a remote node for a specific room.
type Tunnel struct {
	RemoteNodeID string
	RoomID       string
	RemoteAddr   *net.UDPAddr
	CreatedAt    time.Time
}

// NewBridge creates a cross-node audio bridge.
func NewBridge(parentCtx context.Context, listenAddr string) (*Bridge, error) {
	ctx, cancel := context.WithCancel(parentCtx)

	addr, err := net.ResolveUDPAddr("udp4", listenAddr)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("bridge: resolve listen addr: %w", err)
	}

	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("bridge: listen udp: %w", err)
	}

	b := &Bridge{
		localAddr: listenAddr,
		localConn: conn,
		tunnels:   make(map[string]*Tunnel, 16),
		ctx:       ctx,
		cancel:    cancel,
	}

	go b.readLoop()

	return b, nil
}

// SetOnPacketReceived sets the handler for RTP packets received from remote nodes.
func (b *Bridge) SetOnPacketReceived(fn func(roomID, userID string, pkt *rtp.Packet)) {
	b.onPacketReceived = fn
}

// CreateTunnel establishes a UDP tunnel to a remote node for a room.
func (b *Bridge) CreateTunnel(remoteNodeID, roomID, remoteAddr string) error {
	addr, err := net.ResolveUDPAddr("udp4", remoteAddr)
	if err != nil {
		return fmt.Errorf("bridge: resolve remote addr: %w", err)
	}

	key := fmt.Sprintf("%s:%s", remoteNodeID, roomID)

	b.mu.Lock()
	defer b.mu.Unlock()

	b.tunnels[key] = &Tunnel{
		RemoteNodeID: remoteNodeID,
		RoomID:       roomID,
		RemoteAddr:   addr,
		CreatedAt:    time.Now(),
	}

	return nil
}

// DestroyTunnel removes a UDP tunnel.
func (b *Bridge) DestroyTunnel(remoteNodeID, roomID string) {
	key := fmt.Sprintf("%s:%s", remoteNodeID, roomID)
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.tunnels, key)
}

// ForwardPacket sends an RTP packet to a remote node via the tunnel.
// The packet is prefixed with a bridge header containing roomID and userID.
func (b *Bridge) ForwardPacket(remoteNodeID, roomID, userID string, pkt *rtp.Packet) error {
	key := fmt.Sprintf("%s:%s", remoteNodeID, roomID)

	b.mu.RLock()
	tunnel, ok := b.tunnels[key]
	b.mu.RUnlock()

	if !ok {
		return fmt.Errorf("bridge: no tunnel for %s", key)
	}

	rtpData, err := pkt.Marshal()
	if err != nil {
		return fmt.Errorf("bridge: marshal rtp: %w", err)
	}

	// Build bridge header: [2B roomID len][roomID][2B userID len][userID][RTP]
	roomIDBytes := []byte(roomID)
	userIDBytes := []byte(userID)
	headerLen := 2 + len(roomIDBytes) + 2 + len(userIDBytes)
	data := make([]byte, headerLen+len(rtpData))
	offset := 0
	data[offset] = byte(len(roomIDBytes) >> 8)
	data[offset+1] = byte(len(roomIDBytes))
	offset += 2
	copy(data[offset:], roomIDBytes)
	offset += len(roomIDBytes)
	data[offset] = byte(len(userIDBytes) >> 8)
	data[offset+1] = byte(len(userIDBytes))
	offset += 2
	copy(data[offset:], userIDBytes)
	offset += len(userIDBytes)
	copy(data[offset:], rtpData)

	_, err = b.localConn.WriteToUDP(data, tunnel.RemoteAddr)
	if err != nil {
		return fmt.Errorf("bridge: write udp: %w", err)
	}

	return nil
}

// Close shuts down the bridge and all tunnels.
func (b *Bridge) Close() error {
	b.cancel()
	return b.localConn.Close()
}

func (b *Bridge) readLoop() {
	defer func() {
		if r := recover(); r != nil {
			// Recovered panic in bridge read loop
		}
	}()

	buf := make([]byte, 1600) // Extra space for header
	for {
		select {
		case <-b.ctx.Done():
			return
		default:
		}

		_ = b.localConn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, _, err := b.localConn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return
		}

		// Parse bridge header: first 2 bytes = roomID length, then roomID,
		// next 2 bytes = userID length, then userID, rest = RTP packet.
		if n < 4 {
			continue
		}
		offset := 0
		roomIDLen := int(buf[offset])<<8 | int(buf[offset+1])
		offset += 2
		if offset+roomIDLen+2 > n {
			continue
		}
		roomID := string(buf[offset : offset+roomIDLen])
		offset += roomIDLen
		userIDLen := int(buf[offset])<<8 | int(buf[offset+1])
		offset += 2
		if offset+userIDLen > n {
			continue
		}
		userID := string(buf[offset : offset+userIDLen])
		offset += userIDLen

		pkt := &rtp.Packet{}
		if parseErr := pkt.Unmarshal(buf[offset:n]); parseErr != nil {
			continue
		}

		if b.onPacketReceived != nil {
			b.onPacketReceived(roomID, userID, pkt)
		}
	}
}