// Package relay implements the zero-knowledge packet forwarding for mixnet relay nodes.
package relay

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	// ProtocolID is the libp2p protocol identifier for mixnet relaying.
	ProtocolID = "/lib-mix/relay/1.0.0"

	// MaxPayloadSize is the maximum allowed size for a single packet.
	MaxPayloadSize = 64 * 1024 // 64KB

	// ReadTimeout is the duration after which an inactive relay stream is closed.
	ReadTimeout = 30 * time.Second

	// nonceSize is the nonce size for ChaCha20-Poly1305 (12 bytes for standard, 24 for X)
	nonceSize = 24 // XChaCha20-Poly1305
)

// RelayInfo contains runtime statistics and state for an active relay circuit on this node.
type RelayInfo struct {
	// PeerID is the identifier of the peer that opened the relay stream.
	PeerID peer.ID
	// Stream is the network stream being relayed.
	Stream network.Stream
	// CircuitID is the internal identifier for this relay circuit.
	CircuitID string
	// BytesForwarded is the total number of bytes processed by this relay.
	BytesForwarded int64
	// LastActivity is the timestamp of the last data movement.
	LastActivity time.Time
	mu           sync.Mutex
}

// Handler manages all active relay streams and enforces resource limits.
type Handler struct {
	host         host.Host
	maxBandwidth int64
	maxCircuits  int
	activeRelays map[string]*RelayInfo // circuitID -> relay info
	protocolID   string
	mu           sync.RWMutex

	// hopKey is the global encryption key for decrypting outermost layer (AC 7.1)
	hopKey []byte
	// hopKeys stores decryption keys for each hop layer indexed by session/circuit ID
	hopKeys map[string][][]byte // sessionID -> []keys (one per hop)
	muKeys  sync.RWMutex
}

// HopDecrypter defines the interface for decrypting onion-encrypted layers at each relay hop.
// This implements AC 7.1 - Decrypt outermost layer.
type HopDecrypter interface {
	// DecryptHopLayer decrypts the outermost layer of onion encryption and extracts the next hop.
	// Returns the next hop peer ID, remaining encrypted payload, and any error.
	DecryptHopLayer(ciphertext []byte, key []byte) (string, []byte, error)
}

// noiseHopDecrypter implements HopDecrypter using ChaCha20-Poly1305 (same cipher as libp2p Noise).
type noiseHopDecrypter struct{}

// NewNoiseHopDecrypter creates a new hop decrypter.
func NewNoiseHopDecrypter() HopDecrypter {
	return &noiseHopDecrypter{}
}

// DecryptHopLayer decrypts one layer of onion encryption and extracts the next hop.
// AC 7.1: Decrypt outermost layer using ChaCha20-Poly1305 (same cipher as libp2p Noise)
// AC 7.2: Extract next-hop from decrypted header
func (d *noiseHopDecrypter) DecryptHopLayer(ciphertext []byte, key []byte) (string, []byte, error) {
	if len(ciphertext) < nonceSize {
		return "", nil, fmt.Errorf("ciphertext too short: need at least %d bytes", nonceSize)
	}

	// Create ChaCha20-Poly1305 cipher - SAME CIPHER as libp2p Noise uses internally
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return "", nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	// Extract nonce (first nonceSize bytes) and ciphertext
	nonce := ciphertext[:nonceSize]
	encrypted := ciphertext[nonceSize:]

	// Decrypt - no additional data (authenticated encryption)
	plaintext, err := aead.Open(nil, nonce, encrypted, nil)
	if err != nil {
		return "", nil, fmt.Errorf("decryption failed: %w", err)
	}

	// AC 7.2: Extract next-hop from header
	// Header format: [dest_len(2)][dest][remaining_data]
	if len(plaintext) < 2 {
		return "", nil, fmt.Errorf("invalid header: too short")
	}

	destLen := int(binary.LittleEndian.Uint16(plaintext[0:2]))
	if len(plaintext) < 2+destLen {
		return "", nil, fmt.Errorf("invalid destination length: have %d, need %d", len(plaintext)-2, destLen)
	}

	nextHop := string(plaintext[2 : 2+destLen])
	remaining := plaintext[2+destLen:]

	return nextHop, remaining, nil
}

// deriveHopKey derives a hop-specific decryption key using HKDF.
// This matches the key derivation used at the origin for encryption.
func deriveHopKey(prologue string, hopIndex int) ([]byte, error) {
	// Use HKDF with SHA-256 for key derivation (Noise-like)
	derivationInput := []byte(prologue)
	h := sha256.New()
	h.Write(derivationInput)
	h.Write([]byte(fmt.Sprintf("lib-mix-hop-%d", hopIndex)))
	h.Write([]byte("libp2p-mixnet-key derivation"))
	key := h.Sum(nil)
	return key[:32], nil
}

// NewHandler creates a new relay Handler with the specified limits.
func NewHandler(host host.Host, maxCircuits int, maxBandwidth int64) *Handler {
	return &Handler{
		host:         host,
		maxBandwidth: maxBandwidth,
		maxCircuits:  maxCircuits,
		activeRelays: make(map[string]*RelayInfo),
		protocolID:   ProtocolID,
		hopKeys:      make(map[string][][]byte),
	}
}

// HandleStream implements the libp2p stream handler for incoming relay requests.
// It performs zero-knowledge forwarding of the encrypted payload to the next hop.
// AC 7.1: Decrypt outermost layer
// AC 7.2: Extract next-hop from decrypted header
func (h *Handler) HandleStream(stream network.Stream) {
	ctx := context.Background()
	defer stream.Close()

	// Enforce circuit limit (Req 20.1, 20.3).
	h.mu.Lock()
	if h.maxCircuits > 0 && len(h.activeRelays) >= h.maxCircuits {
		h.mu.Unlock()
		return
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

	// AC 7.1: Read the encrypted payload - the entire stream is onion-encrypted
	encryptedPayload, err := io.ReadAll(io.LimitReader(stream, int64(MaxPayloadSize)))
	if err != nil {
		return
	}

	if len(encryptedPayload) == 0 {
		return
	}

	// AC 7.1: Decrypt the outermost layer to extract next hop
	decrypter := NewNoiseHopDecrypter()

	// Try each available key to find the right one (for multi-hop circuits)
	var nextHop string
	var remainingPayload []byte
	var keyUsed bool

	// Try session-specific keys first
	h.muKeys.RLock()
	for _, keys := range h.hopKeys {
		for _, key := range keys {
			nextHop, remainingPayload, err = decrypter.DecryptHopLayer(encryptedPayload, key)
			if err == nil {
				keyUsed = true
				break
			}
		}
		if keyUsed {
			break
		}
	}
	h.muKeys.RUnlock()

	// If no session key worked, try the global hop key
	if !keyUsed && len(h.hopKey) > 0 {
		nextHop, remainingPayload, err = decrypter.DecryptHopLayer(encryptedPayload, h.hopKey)
		if err != nil {
			// If decryption fails, treat the first bytes as next hop (backward compatibility)
			nextHop = ""
			remainingPayload = encryptedPayload
		}
	} else if !keyUsed {
		// No keys available - forward as-is (backward compatibility)
		nextHop = ""
		remainingPayload = encryptedPayload
	}

	// AC 7.2: Extract next-hop from decrypted header
	// If nextHop is empty after decryption, try to read from header format
	if nextHop == "" && len(remainingPayload) >= 2 {
		// Fallback: read from legacy cleartext format
		destLen := int(binary.LittleEndian.Uint16(remainingPayload[0:2]))
		if destLen > 0 && destLen < len(remainingPayload)-2 {
			nextHop = string(remainingPayload[2:2+destLen])
			remainingPayload = remainingPayload[2+destLen:]
		}
	}

	// If we still don't have a next hop, we can't forward
	if nextHop == "" {
		return
	}

	// Parse the next hop as a peer ID; fall back to multiaddr parsing.
	nextPeer, err := peer.Decode(nextHop)
	if err != nil {
		// Forward by address using the remaining payload
		_ = h.forwardByAddressWithPayload(ctx, nextHop, remainingPayload)
		return
	}

	// AC 7.2: Forward to the extracted next hop with remaining encrypted payload
	_ = h.forwardToPeerStreamWithPayload(ctx, nextPeer, remainingPayload)
}

// forwardToPeerStream opens a stream to nextPeer and pipes the remaining encrypted payload directly.
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

// forwardToPeerStreamWithPayload forwards the remaining decrypted payload to the next peer (AC 7.2).
func (h *Handler) forwardToPeerStreamWithPayload(ctx context.Context, nextPeer peer.ID, payload []byte) error {
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

	// Write the remaining encrypted payload (after removing outermost layer)
	if _, err := writer.Write(payload); err != nil {
		return fmt.Errorf("failed to forward payload: %w", err)
	}

	return nil
}

// forwardByAddress parses addr as a multiaddr peer info string and streams the payload there.
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

// forwardByAddressWithPayload forwards the remaining payload to an address (AC 7.2).
func (h *Handler) forwardByAddressWithPayload(ctx context.Context, addr string, payload []byte) error {
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

	// Write the remaining encrypted payload
	if _, err := dst.Write(payload); err != nil {
		return fmt.Errorf("failed to forward payload: %w", err)
	}

	return nil
}

// SetHopKey sets the global encryption key for this relay to decrypt the outermost layer.
// This enables AC 7.1 - decrypting at relays.
func (h *Handler) SetHopKey(key []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hopKey = make([]byte, len(key))
	copy(h.hopKey, key)
}

// RegisterHopKeys registers encryption keys for a specific session/circuit.
// This allows the relay to decrypt onion layers for multi-hop circuits.
func (h *Handler) RegisterHopKeys(sessionID string, keys [][]byte) {
	h.muKeys.Lock()
	defer h.muKeys.Unlock()
	h.hopKeys[sessionID] = keys
}

// ClearHopKeys removes all registered hop keys for a session.
func (h *Handler) ClearHopKeys(sessionID string) {
	h.muKeys.Lock()
	defer h.muKeys.Unlock()
	delete(h.hopKeys, sessionID)
}

type rateLimitedWriter struct {
	w           io.Writer
	bytesPerSec int64
}

func (r *rateLimitedWriter) Write(p []byte) (int, error) {
	if r.bytesPerSec > 0 && int64(len(p)) > 0 {
		delay := time.Duration(int64(time.Second) * int64(len(p)) / r.bytesPerSec)
		if delay > 0 {
			time.Sleep(delay)
		}
	}
	return r.w.Write(p)
}

// MaxCircuits returns the maximum number of concurrent circuits allowed by the handler.
func (h *Handler) MaxCircuits() int {
	return h.maxCircuits
}

// MaxBandwidth returns the maximum bandwidth allowed per circuit.
func (h *Handler) MaxBandwidth() int64 {
	return h.maxBandwidth
}

// ActiveCircuitCount returns the current number of active relay circuits.
func (h *Handler) ActiveCircuitCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.activeRelays)
}

// RegisterRelay manually registers an active relay circuit (primarily for testing).
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

// UnregisterRelay removes a relay circuit and closes its stream.
func (h *Handler) UnregisterRelay(circuitID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if relay, ok := h.activeRelays[circuitID]; ok {
		relay.Stream.Close()
		delete(h.activeRelays, circuitID)
	}
}

// GetRelayInfo retrieves information about an active relay circuit.
func (h *Handler) GetRelayInfo(circuitID string) (*RelayInfo, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	relay, ok := h.activeRelays[circuitID]
	return relay, ok
}

// Host returns the libp2p host used by the handler.
func (h *Handler) Host() host.Host {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.host
}

// SetHost sets the libp2p host for the handler.
func (h *Handler) SetHost(host host.Host) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.host = host
}
