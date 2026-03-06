// Package relay implements the zero-knowledge packet forwarding for mixnet relay nodes.
package relay

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	// ProtocolID is the libp2p protocol identifier for mixnet relaying.
	ProtocolID = "/lib-mix/relay/1.0.0"
	// FinalProtocolID is the protocol identifier used when forwarding to the destination.
	FinalProtocolID = "/lib-mix/1.0.0"

	// MaxPayloadSize is the maximum allowed size for a single packet.
	MaxPayloadSize = 64 * 1024 // 64KB

	// maxEncryptedPayloadSize is the maximum allowed size for an encrypted frame payload.
	// The 4x multiplier accounts for encryption overhead and multi-hop wrapping.
	maxEncryptedPayloadSize = MaxPayloadSize * 4

	// ReadTimeout is the duration after which an inactive relay stream is closed.
	ReadTimeout = 30 * time.Second

	// nonceSize is the nonce size for ChaCha20-Poly1305 (12 bytes for standard, 24 for X)
	nonceSize = 24 // XChaCha20-Poly1305
)

const (
	msgTypeData     byte = 0x00
	msgTypeCloseReq byte = 0x01
	msgTypeCloseAck byte = 0x02
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
	host              host.Host
	maxBandwidth      int64
	maxCircuits       int
	useRCMgr          bool
	serviceName       string
	activeRelays      map[string]*RelayInfo // circuitID -> relay info
	protocolID        string
	mu                sync.RWMutex
	muKeys            sync.RWMutex
	circuitKeys       map[string][]byte // circuitID -> hop key
	waitBandwidth     func(context.Context, int64) error
	recordBandwidth   func(string, int64)
	reportUtilization func(int)
}

// NewHandler creates a new relay Handler with the specified limits.
func NewHandler(host host.Host, maxCircuits int, maxBandwidth int64) *Handler {
	return &Handler{
		host:         host,
		maxBandwidth: maxBandwidth,
		maxCircuits:  maxCircuits,
		useRCMgr:     true,
		serviceName:  "mixnet-relay",
		activeRelays: make(map[string]*RelayInfo),
		protocolID:   ProtocolID,
		circuitKeys:  make(map[string][]byte),
	}
}

// HandleStream implements the libp2p stream handler for incoming relay requests.
// It performs zero-knowledge forwarding of the encrypted payload to the next hop.
// AC 7.1: Decrypt outermost layer
// AC 7.2: Extract next-hop from decrypted header
func (h *Handler) HandleStream(stream network.Stream) {
	ctx := context.Background()
	defer stream.Close()

	// Enforce compatibility circuit limits only when rcmgr integration is disabled.
	h.mu.Lock()
	if !h.useRCMgr && h.maxCircuits > 0 && len(h.activeRelays) >= h.maxCircuits {
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
	if h.reportUtilization != nil {
		h.reportUtilization(len(h.activeRelays))
	}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.activeRelays, circuitID)
		if h.reportUtilization != nil {
			h.reportUtilization(len(h.activeRelays))
		}
		h.mu.Unlock()
	}()

	// Set a read deadline so the relay cannot be held open indefinitely (Req 6.3).
	stream.SetDeadline(time.Now().Add(ReadTimeout))

	reader := bufio.NewReader(stream)
	var dst network.Stream
	var dstPeer peer.ID
	var dstIsFinal bool

	defer func() {
		if dst != nil {
			_ = dst.Close()
		}
	}()

	for {
		circuitID, encPayload, releaseMem, err := readEncryptedFrame(reader, stream.Scope())
		if err != nil {
			return
		}
		err = func() error {
			if releaseMem != nil {
				defer releaseMem()
			}
			if h.recordBandwidth != nil {
				h.recordBandwidth("in", int64(len(encPayload)))
			}

			key := h.getCircuitKey(circuitID)
			if len(key) == 0 {
				return fmt.Errorf("missing circuit key")
			}

			plaintext, err := decryptHopPayload(key, encPayload)
			if err != nil {
				return err
			}

			isFinal, nextHop, innerPayload, err := parseHopPayload(plaintext)
			if err != nil {
				return err
			}

			// Determine protocol based on whether this is the final hop.
			nextProto := ProtocolID
			if isFinal {
				nextProto = FinalProtocolID
			}

			// Parse the next hop as a peer ID; fall back to multiaddr parsing.
			nextPeer, err := peer.Decode(nextHop)
			if err != nil {
				if dst != nil {
					_ = dst.Close()
					dst = nil
				}
				return h.forwardByAddressEncrypted(ctx, nextHop, circuitID, innerPayload, nextProto)
			}

			// Keep per-circuit streams open; rotate only when route target/mode changes.
			if dst == nil || dstPeer != nextPeer || dstIsFinal != isFinal {
				if dst != nil {
					_ = dst.Close()
				}
				s, err := h.openStream(ctx, nextPeer, nextProto)
				if err != nil {
					return err
				}
				dst = s
				dstPeer = nextPeer
				dstIsFinal = isFinal
			}

			// Apply bandwidth limit as a rate-limited writer if configured (Req 20.2, 20.4).
			var writer io.Writer = dst
			h.mu.RLock()
			maxBandwidth := h.maxBandwidth
			waitBandwidth := h.waitBandwidth
			recordBandwidth := h.recordBandwidth
			h.mu.RUnlock()
			if maxBandwidth > 0 {
				writer = &rateLimitedWriter{w: dst, bytesPerSec: maxBandwidth}
			}
			if waitBandwidth != nil {
				if err := waitBandwidth(ctx, int64(len(innerPayload))); err != nil {
					return err
				}
			}

			if isFinal {
				n, err := writer.Write(innerPayload)
				if err != nil {
					return err
				}
				if recordBandwidth != nil {
					recordBandwidth("out", int64(n))
				}
				if len(innerPayload) > 0 && innerPayload[0] == msgTypeCloseReq {
					if err := waitForCloseAck(dst); err != nil {
						return err
					}
					if _, err := stream.Write([]byte{msgTypeCloseAck}); err != nil {
						return err
					}
					_ = dst.Close()
					return io.EOF
				}
			} else {
				n, err := writeEncryptedFrame(writer, circuitID, innerPayload)
				if err != nil {
					return err
				}
				if recordBandwidth != nil {
					recordBandwidth("out", int64(n))
				}
			}

			return nil
		}()
		if err == io.EOF {
			return
		}
		if err != nil {
			return
		}
	}
}

func (h *Handler) openStream(ctx context.Context, nextPeer peer.ID, protoID string) (network.Stream, error) {
	h.mu.RLock()
	host := h.host
	useRCMgr := h.useRCMgr
	serviceName := h.serviceName
	h.mu.RUnlock()

	if host == nil {
		return nil, fmt.Errorf("no host configured")
	}
	if host.Network().Connectedness(nextPeer) != network.Connected {
		connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := host.Connect(connectCtx, peer.AddrInfo{ID: nextPeer}); err != nil {
			return nil, fmt.Errorf("failed to connect to next hop: %w", err)
		}
	}

	// Pre-admit outbound stream with rcmgr so resource policies are enforced centrally.
	if useRCMgr {
		rm := host.Network().ResourceManager()
		scope, err := rm.OpenStream(nextPeer, network.DirOutbound)
		if err != nil {
			return nil, fmt.Errorf("rcmgr rejected outbound stream: %w", err)
		}
		if err := scope.SetProtocol(protocol.ID(protoID)); err != nil {
			scope.Done()
			return nil, fmt.Errorf("rcmgr protocol scope setup failed: %w", err)
		}
		// Best effort. Service names are optional in rcmgr.
		_ = scope.SetService(serviceName)
		defer scope.Done()
	}

	s, err := host.NewStream(ctx, nextPeer, protocol.ID(protoID))
	if err != nil {
		return nil, err
	}
	// Tag the stream's own scope with service so resource policies are enforced correctly.
	if useRCMgr {
		_ = s.Scope().SetService(serviceName)
	}
	return s, nil
}

func (h *Handler) forwardByAddressEncrypted(ctx context.Context, addr string, circuitID string, payload []byte, protoID string) error {
	h.mu.RLock()
	host := h.host
	maxBandwidth := h.maxBandwidth
	waitBandwidth := h.waitBandwidth
	recordBandwidth := h.recordBandwidth
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

	dst, err := host.NewStream(ctx, addrInfo.ID, protocol.ID(protoID))
	if err != nil {
		return fmt.Errorf("failed to open stream: %w", err)
	}
	defer dst.Close()

	var writer io.Writer = dst
	if maxBandwidth > 0 {
		writer = &rateLimitedWriter{w: dst, bytesPerSec: maxBandwidth}
	}
	if waitBandwidth != nil {
		if err := waitBandwidth(ctx, int64(len(payload))); err != nil {
			return err
		}
	}

	n, err := writeEncryptedFrame(writer, circuitID, payload)
	if err != nil {
		return err
	}
	if recordBandwidth != nil {
		recordBandwidth("out", int64(n))
	}
	return nil
}

func waitForCloseAck(stream network.Stream) error {
	_ = stream.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 1)
	if _, err := io.ReadFull(stream, buf); err != nil {
		return err
	}
	if buf[0] != msgTypeCloseAck {
		return fmt.Errorf("unexpected close ack: %x", buf[0])
	}
	return nil
}

func readEncryptedFrame(r *bufio.Reader, scope network.StreamScope) (string, []byte, func(), error) {
	cidLen, err := r.ReadByte()
	if err != nil {
		return "", nil, nil, err
	}
	if cidLen == 0 {
		return "", nil, nil, fmt.Errorf("empty circuit id")
	}
	cid := make([]byte, int(cidLen))
	if _, err := io.ReadFull(r, cid); err != nil {
		return "", nil, nil, err
	}
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, lenBuf); err != nil {
		return "", nil, nil, err
	}
	payloadLen := int(binary.LittleEndian.Uint32(lenBuf))
	if payloadLen <= 0 || payloadLen > maxEncryptedPayloadSize {
		return "", nil, nil, fmt.Errorf("invalid encrypted payload length")
	}

	release := func() {}
	if scope != nil {
		if err := scope.ReserveMemory(payloadLen, network.ReservationPriorityMedium); err != nil {
			return "", nil, nil, fmt.Errorf("rcmgr inbound memory reservation failed: %w", err)
		}
		release = func() { scope.ReleaseMemory(payloadLen) }
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		release()
		return "", nil, nil, err
	}
	return string(cid), payload, release, nil
}

func writeEncryptedFrame(w io.Writer, circuitID string, payload []byte) (int, error) {
	if len(circuitID) == 0 || len(circuitID) > 255 {
		return 0, fmt.Errorf("invalid circuit id")
	}
	if len(payload) > maxEncryptedPayloadSize {
		return 0, fmt.Errorf("payload too large: %d bytes exceeds limit of %d", len(payload), maxEncryptedPayloadSize)
	}
	header := make([]byte, 1+len(circuitID)+4)
	header[0] = byte(len(circuitID))
	copy(header[1:], []byte(circuitID))
	binary.LittleEndian.PutUint32(header[1+len(circuitID):], uint32(len(payload)))
	hn, err := w.Write(header)
	if err != nil {
		return hn, err
	}
	pn, err := w.Write(payload)
	return hn + pn, err
}

func decryptHopPayload(key []byte, payload []byte) ([]byte, error) {
	if len(payload) < nonceSize {
		return nil, fmt.Errorf("payload too short")
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce := payload[:nonceSize]
	ciphertext := payload[nonceSize:]
	return aead.Open(nil, nonce, ciphertext, nil)
}

func parseHopPayload(plaintext []byte) (bool, string, []byte, error) {
	if len(plaintext) < 1+2 {
		return false, "", nil, fmt.Errorf("plaintext too short")
	}
	isFinal := plaintext[0] == 1
	nextLen := int(binary.LittleEndian.Uint16(plaintext[1:3]))
	if len(plaintext) < 3+nextLen {
		return false, "", nil, fmt.Errorf("invalid next hop length")
	}
	nextHop := string(plaintext[3 : 3+nextLen])
	inner := plaintext[3+nextLen:]
	return isFinal, nextHop, inner, nil
}

// HandleKeyExchange registers hop keys for a circuit using a Noise XX handshake.
func (h *Handler) HandleKeyExchange(stream network.Stream) {
	defer stream.Close()
	payload, err := runNoiseXXResponder(context.Background(), stream)
	if err != nil {
		return
	}
	circuitID, key, err := decodeKeyExchangePayload(payload)
	if err != nil {
		return
	}
	h.setCircuitKey(circuitID, key)
}

func (h *Handler) setCircuitKey(circuitID string, key []byte) {
	h.muKeys.Lock()
	defer h.muKeys.Unlock()
	h.circuitKeys[circuitID] = key
}

func (h *Handler) getCircuitKey(circuitID string) []byte {
	h.muKeys.RLock()
	defer h.muKeys.RUnlock()
	return h.circuitKeys[circuitID]
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

// EnableLibp2pResourceManager toggles rcmgr-based admission and accounting.
func (h *Handler) EnableLibp2pResourceManager(enabled bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.useRCMgr = enabled
}

// SetResourceServiceName sets the rcmgr service name for stream scopes.
func (h *Handler) SetResourceServiceName(name string) {
	if name == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.serviceName = name
}

// SetBandwidthBackpressure sets a callback used to enforce bandwidth backpressure.
func (h *Handler) SetBandwidthBackpressure(fn func(context.Context, int64) error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.waitBandwidth = fn
}

// SetBandwidthRecorder sets a callback used to record inbound/outbound bandwidth.
func (h *Handler) SetBandwidthRecorder(fn func(direction string, bytes int64)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recordBandwidth = fn
}

// SetUtilizationReporter sets a callback to publish active relay circuit utilization.
func (h *Handler) SetUtilizationReporter(fn func(activeCircuits int)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reportUtilization = fn
}
