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
	Stream       network.Stream
	CircuitID    string
	BytesForwarded int64
	LastActivity time.Time
	mu           sync.Mutex
}

// Handler handles relay traffic - zero knowledge forwarding
type Handler struct {
	host         host.Host
	maxBandwidth int64
	maxCircuits  int
	activeRelays map[string]*RelayInfo // circuitID -> relay info
	protocolID   string
	mu           sync.RWMutex
}

// NewHandler creates a new relay handler
func NewHandler(host host.Host, maxCircuits int, maxBandwidth int64) *Handler {
	return &Handler{
		host:         host,
		maxBandwidth: maxBandwidth,
		maxCircuits:  maxCircuits,
		activeRelays: make(map[string]*RelayInfo),
		protocolID:   ProtocolID,
	}
}

// HandleStream handles an incoming relay stream.
// Implements zero-knowledge forwarding: read next-hop destination and stream
// the remaining encrypted payload to the next hop without buffering (Req 7.4, 20.4).
func (h *Handler) HandleStream(ctx context.Context, stream network.Stream) error {
	defer stream.Close()

	// Enforce circuit limit (Req 20.1, 20.3).
	h.mu.Lock()
	if h.maxCircuits > 0 && len(h.activeRelays) >= h.maxCircuits {
		h.mu.Unlock()
		return fmt.Errorf("max circuits reached (%d)", h.maxCircuits)
	}
	circuitID := fmt.Sprintf("relay-%d", len(h.activeRelays))
	h.activeRelays[circuitID] = &RelayInfo{
		PeerID:       stream.Conn().RemotePeer(),
		Stream:       stream,
		CircuitID:    circuitID,
		LastActivity: time.Now(),
	}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.activeRelays, circuitID)
		h.mu.Unlock()
	}()

	// Set a read deadline so the relay cannot be held open indefinitely (Req 6.3).
	stream.SetDeadline(time.Now().Add(ReadTimeout))

	// Read destination length (2 bytes, little-endian).
	destLenBuf := make([]byte, 2)
	if _, err := io.ReadFull(stream, destLenBuf); err != nil {
		return fmt.Errorf("failed to read destination length: %w", err)
	}

	destLen := binary.LittleEndian.Uint16(destLenBuf)
	if destLen == 0 || destLen > 512 {
		return fmt.Errorf("invalid destination length: %d", destLen)
	}

	destBuf := make([]byte, destLen)
	if _, err := io.ReadFull(stream, destBuf); err != nil {
		return fmt.Errorf("failed to read destination: %w", err)
	}

	nextHop := string(destBuf)

	// Parse the next hop as a peer ID; fall back to multiaddr parsing.
	nextPeer, err := peer.Decode(nextHop)
	if err != nil {
		return h.forwardByAddress(ctx, nextHop, stream)
	}

	return h.forwardToPeerStream(ctx, nextPeer, stream)
}

// forwardToPeerStream opens a stream to nextPeer and pipes the remaining
// encrypted payload directly without buffering (Req 7.4, 20.4).
func (h *Handler) forwardToPeerStream(ctx context.Context, nextPeer peer.ID, src network.Stream) error {
	h.mu.RLock()
	host := h.host
	maxBandwidth := h.maxBandwidth
	h.mu.RUnlock()

	if host == nil {
		return fmt.Errorf("no host configured")
	}

	// Check if we're already connected
	if host.Network().Connectedness(nextPeer) != network.Connected {
		connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := host.Connect(connectCtx, peer.AddrInfo{ID: nextPeer}); err != nil {
			return fmt.Errorf("failed to connect to next hop: %w", err)
		}
	}

	dst, err := host.NewStream(ctx, nextPeer, ProtocolID)
	if err != nil {
		return fmt.Errorf("failed to open stream to %s: %w", nextPeer, err)
	}
	defer dst.Close()

	// Apply bandwidth limit as a rate-limited writer if configured (Req 20.2, 20.4).
	var writer io.Writer = dst
	if maxBandwidth > 0 {
		writer = &rateLimitedWriter{w: dst, bytesPerSec: maxBandwidth}
	}

	if _, err := io.Copy(writer, src); err != nil {
		return fmt.Errorf("failed to forward payload: %w", err)
	}
	return nil
}

// forwardByAddress parses addr as a multiaddr peer info string and streams
// the payload there without buffering.
func (h *Handler) forwardByAddress(ctx context.Context, addr string, src network.Stream) error {
	h.mu.RLock()
	host := h.host
	h.mu.RUnlock()

	if host == nil {
		return fmt.Errorf("no host configured")
	}

	addrInfo, err := peer.AddrInfoFromString(addr)
	if err != nil {
		return fmt.Errorf("failed to parse address: %w", err)
	}

	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := host.Connect(connectCtx, *addrInfo); err != nil {
		return fmt.Errorf("failed to connect to %s: %w", addr, err)
	}

	dst, err := host.NewStream(ctx, addrInfo.ID, ProtocolID)
	if err != nil {
		return fmt.Errorf("failed to open stream: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("failed to forward payload: %w", err)
	}
	return nil
}

// rateLimitedWriter writes to the underlying writer while enforcing a maximum
// bytes-per-second rate by sleeping between writes (Req 20.2).
type rateLimitedWriter struct {
	w           io.Writer
	bytesPerSec int64
}

func (r *rateLimitedWriter) Write(p []byte) (n int, err error) {
	// Simple token-bucket approximation: sleep for (len/bytesPerSec) seconds.
	if r.bytesPerSec > 0 && int64(len(p)) > 0 {
		delay := time.Duration(int64(time.Second) * int64(len(p)) / r.bytesPerSec)
		if delay > 0 {
			time.Sleep(delay)
		}
	}
	return r.w.Write(p)
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
func (h *Handler) RegisterRelay(circuitID string, peerID peer.ID, stream network.Stream) error {
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
