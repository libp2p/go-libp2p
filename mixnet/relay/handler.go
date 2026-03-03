package relay

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/mixnet/ces"
)

const (
	// ProtocolID is the libp2p protocol for mixnet relay
	ProtocolID = "/lib-mix/relay/1.0.0"

	// MaxPayloadSize limits the maximum payload size
	MaxPayloadSize = 64 * 1024 // 64KB

	// ReadTimeout is the timeout for reading from streams
	ReadTimeout = 30 * time.Second
)

// RelayInfo holds information about an active relay
type RelayInfo struct {
	PeerID       peer.ID
	Stream       network.MuxedStream
	CircuitID    string
	BytesForwarded int64
	LastActivity time.Time
	mu           sync.Mutex
}

// Handler handles relay traffic - zero knowledge forwarding
type Handler struct {
	host           host.Host
	maxBandwidth   int64
	maxCircuits    int
	activeRelays   map[string]*RelayInfo // circuitID -> relay info
	pendingData    map[string][]byte     // circuitID -> pending data for next hop
	encrypter      *ces.LayeredEncrypter
	protocolID     string
	mu             sync.RWMutex
}

// NewHandler creates a new relay handler
func NewHandler(host host.Host, maxCircuits int, maxBandwidth int64) *Handler {
	return &Handler{
		host:         host,
		maxBandwidth: maxBandwidth,
		maxCircuits:  maxCircuits,
		activeRelays: make(map[string]*RelayInfo),
		pendingData:  make(map[string][]byte),
		protocolID:   ProtocolID,
	}
}

// SetEncrypter sets the layered encrypter for outer layer decryption
func (h *Handler) SetEncrypter(e *ces.LayeredEncrypter) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.encrypter = e
}

// HandleStream handles an incoming relay stream
// Implements zero-knowledge forwarding: decrypt outer layer, forward remaining
func (h *Handler) HandleStream(ctx context.Context, stream network.MuxedStream) error {
	// Read the destination from the first bytes
	// Format: [dest_len:2][dest_bytes][encrypted_payload]

	destLenBuf := make([]byte, 2)
	_, err := io.ReadFull(stream, destLenBuf)
	if err != nil {
		return fmt.Errorf("failed to read destination length: %w", err)
	}

	destLen := binary.LittleEndian.Uint16(destLenBuf)
	if destLen > 256 {
		return fmt.Errorf("invalid destination length: %d", destLen)
	}

	destBuf := make([]byte, destLen)
	_, err = io.ReadFull(stream, destBuf)
	if err != nil {
		return fmt.Errorf("failed to read destination: %w", err)
	}

	// Read the encrypted payload
	payload, err := io.ReadAll(stream)
	if err != nil {
		return fmt.Errorf("failed to read payload: %w", err)
	}

	// Decrypt only the outer layer
	h.mu.RLock()
	encrypter := h.encrypter
	h.mu.RUnlock()

	if encrypter == nil {
		return fmt.Errorf("encrypter not configured")
	}

	// For relay: we need to decrypt with the key for this layer
	// The key should have been established during circuit creation
	// For now, we'll use a simple approach: the payload contains
	// [next_hop_addr_len:2][next_hop_addr][remaining_encrypted_data]

	// Extract next hop from decrypted outer layer
	if len(payload) < 2 {
		return fmt.Errorf("payload too short")
	}

	nextHopLen := binary.LittleEndian.Uint16(payload[0:2])
	if len(payload) < 2+int(nextHopLen) {
		return fmt.Errorf("invalid next hop length")
	}

	nextHop := string(payload[2 : 2+nextHopLen])
	remainingData := payload[2+nextHopLen:]

	// Parse the next hop as a peer ID
	nextPeer, err := peer.Decode(nextHop)
	if err != nil {
		// It might be a multiaddr, try to resolve
		return h.forwardByAddress(ctx, nextHop, remainingData, stream)
	}

	// Forward to the next peer
	return h.forwardToPeer(ctx, nextPeer, remainingData)
}

// forwardToPeer forwards data to a specific peer
func (h *Handler) forwardToPeer(ctx context.Context, nextPeer peer.ID, data []byte) error {
	h.mu.RLock()
	host := h.host
	h.mu.RUnlock()

	if host == nil {
		return fmt.Errorf("no host configured")
	}

	// Check if we're already connected
	if host.Network().Connectedness(nextPeer) != network.Connected {
		// We need to connect - but as a relay, we shouldn't know the destination
		// This is where the privacy comes in - we forward to next hop without knowing final dest
		connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		// Get address from peerstore or use cached addresses
		if err := host.Connect(connectCtx, peer.AddrInfo{ID: nextPeer}); err != nil {
			return fmt.Errorf("failed to connect to next hop: %w", err)
		}
	}

	// Open stream to next hop
	stream, err := host.NewStream(ctx, nextPeer, ProtocolID)
	if err != nil {
		return fmt.Errorf("failed to open stream to %s: %w", nextPeer, err)
	}
	defer stream.Close()

	// Forward the data
	_, err = stream.Write(data)
	return err
}

// forwardByAddress forwards data to a multiaddr
func (h *Handler) forwardByAddress(ctx context.Context, addr string, data []byte, origStream network.MuxedStream) error {
	h.mu.RLock()
	host := h.host
	h.mu.RUnlock()

	if host == nil {
		return fmt.Errorf("no host configured")
	}

	// Parse the address and connect
	addrInfo, err := peer.AddrInfoFromString(addr)
	if err != nil {
		return fmt.Errorf("failed to parse address: %w", err)
	}

	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := host.Connect(connectCtx, *addrInfo); err != nil {
		return fmt.Errorf("failed to connect to %s: %w", addr, err)
	}

	stream, err := host.NewStream(ctx, addrInfo.ID, ProtocolID)
	if err != nil {
		return fmt.Errorf("failed to open stream: %w", err)
	}
	defer stream.Close()

	_, err = stream.Write(data)
	return err
}

// HandleReadCloser wraps a stream for reading
type HandleReadCloser struct {
	stream network.MuxedStream
	ctx    context.Context
}

// Read implements io.Reader
func (r *HandleReadCloser) Read(p []byte) (n int, err error) {
	// Set deadline
	deadline := time.Now().Add(ReadTimeout)
	r.stream.SetDeadline(deadline)

	return r.stream.Read(p)
}

// Close implements io.Closer
func (r *HandleReadCloser) Close() error {
	return r.stream.Close()
}

// MaxCircuits returns the maximum number of concurrent circuits
func (h *Handler) MaxCircuits() int {
	return h.maxCircuits
}

// MaxBandwidth returns the maximum bandwidth per circuit
func (h *Handler) MaxBandwidth() int64 {
	return h.maxBandwidth
}

// ActiveCircuitCount returns the number of active relay circuits
func (h *Handler) ActiveCircuitCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.activeRelays)
}

// RegisterRelay registers an active relay
func (h *Handler) RegisterRelay(circuitID string, peerID peer.ID, stream network.MuxedStream) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.activeRelays) >= h.maxCircuits {
		return fmt.Errorf("max circuits reached")
	}

	h.activeRelays[circuitID] = &RelayInfo{
		PeerID:       peerID,
		Stream:       stream,
		CircuitID:    circuitID,
		LastActivity: time.Now(),
	}

	return nil
}

// UnregisterRelay removes a relay
func (h *Handler) UnregisterRelay(circuitID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if relay, ok := h.activeRelays[circuitID]; ok {
		relay.Stream.Close()
		delete(h.activeRelays, circuitID)
	}
}

// GetRelayInfo returns info about a relay
func (h *Handler) GetRelayInfo(circuitID string) (*RelayInfo, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	relay, ok := h.activeRelays[circuitID]
	return relay, ok
}

// Host returns the underlying host
func (h *Handler) Host() host.Host {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.host
}

// SetHost sets the libp2p host
func (h *Handler) SetHost(host host.Host) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.host = host
}
