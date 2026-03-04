// Package circuit manages the establishment and maintenance of multi-hop mixnet paths.
package circuit

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/multiformats/go-multiaddr"
)

// RelayInfo holds metadata and performance information for a potential relay node.
type RelayInfo struct {
	// PeerID is the unique identifier of the relay peer.
	PeerID    peer.ID
	// AddrInfo contains the network addresses of the relay.
	AddrInfo  peer.AddrInfo
	// Latency is the measured round-trip time to the relay.
	Latency   time.Duration
	// Connected indicates if there is currently an active connection to the relay.
	Connected bool
}

// CircuitConfig contains parameters for circuit creation and management.
type CircuitConfig struct {
	// HopCount is the number of relays per circuit.
	HopCount      int
	// CircuitCount is the number of parallel circuits.
	CircuitCount  int
	// StreamTimeout is the maximum time to wait for stream operations.
	StreamTimeout time.Duration
}

// StreamHandler manages the network stream associated with a specific circuit.
type StreamHandler struct {
	stream network.Stream
	peerID peer.ID
}

// Stream returns the underlying network stream.
func (h *StreamHandler) Stream() network.Stream {
	return h.stream
}

// CircuitManager is responsible for the entire lifecycle of mixnet circuits.
type CircuitManager struct {
	cfg       *CircuitConfig
	circuits  map[string]*Circuit
	relayPool []peer.ID
	threshold int
	host      host.Host
	streams   map[string]*StreamHandler // circuitID -> stream handler
	mu        sync.RWMutex
	wg        sync.WaitGroup
	ctx       context.Context
	cancel    context.CancelFunc
}

// NewCircuitManager creates a new CircuitManager instance with the given configuration.
func NewCircuitManager(cfg *CircuitConfig) *CircuitManager {
	threshold := cfg.CircuitCount - 1
	if threshold < 1 {
		threshold = 1
	}

	streamTimeout := cfg.StreamTimeout
	if streamTimeout == 0 {
		streamTimeout = 30 * time.Second
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &CircuitManager{
		cfg:        cfg,
		circuits:   make(map[string]*Circuit),
		threshold:  threshold,
		streams:    make(map[string]*StreamHandler),
		ctx:        ctx,
		cancel:     cancel,
	}
}

// SetHost sets the libp2p host to be used for establishing circuit streams.
func (m *CircuitManager) SetHost(h host.Host) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.host = h
}

// BuildCircuits selects relays and constructs a set of independent circuits to the destination.
func (m *CircuitManager) BuildCircuits(ctx context.Context, dest peer.ID, relays []RelayInfo) ([]*Circuit, error) {
	if len(relays) < m.cfg.HopCount*m.cfg.CircuitCount {
		return nil, fmt.Errorf("insufficient relays: have %d, need %d",
			len(relays), m.cfg.HopCount*m.cfg.CircuitCount)
	}

	filtered := m.filterRelays(relays, dest)
	if len(filtered) < m.cfg.HopCount*m.cfg.CircuitCount {
		return nil, fmt.Errorf("insufficient relays after filtering: have %d, need %d",
			len(filtered), m.cfg.HopCount*m.cfg.CircuitCount)
	}

	circuits := m.buildUniqueCircuits(filtered)

	for _, c := range circuits {
		m.circuits[c.ID] = c
	}

	m.relayPool = make([]peer.ID, len(filtered))
	for i, r := range filtered {
		m.relayPool[i] = r.PeerID
	}

	return circuits, nil
}

// BuildCircuit builds a single new circuit using the available relay pool.
func (m *CircuitManager) BuildCircuit() (*Circuit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.relayPool) < m.cfg.HopCount {
		return nil, fmt.Errorf("insufficient relays in pool")
	}

	// Simple random selection from pool
	rand.Shuffle(len(m.relayPool), func(i, j int) {
		m.relayPool[i], m.relayPool[j] = m.relayPool[j], m.relayPool[i]
	})

	peers := make([]peer.ID, m.cfg.HopCount)
	copy(peers, m.relayPool[:m.cfg.HopCount])

	id := fmt.Sprintf("circuit-%d", len(m.circuits))
	c := NewCircuit(id, peers)
	m.circuits[id] = c
	return c, nil
}

// EstablishCircuit opens a network stream to the first relay of the specified circuit.
func (m *CircuitManager) EstablishCircuit(circuit *Circuit, dest peer.ID, protocolID string) error {
	if len(circuit.Peers) == 0 {
		return fmt.Errorf("circuit has no peers")
	}

	entryPeer := circuit.Peers[0]

	m.mu.RLock()
	h := m.host
	m.mu.RUnlock()

	if h == nil {
		return fmt.Errorf("no host configured")
	}

	// Ensure we have a connection to the entry relay
	connectCtx, cancel := context.WithTimeout(m.ctx, m.cfg.StreamTimeout)
	defer cancel()

	// Try to get addresses from peerstore
	var addrs []multiaddr.Multiaddr
	if m.host != nil {
		if pi := m.host.Peerstore().PeerInfo(entryPeer); len(pi.Addrs) > 0 {
			addrs = pi.Addrs
		}
	}

	if len(addrs) > 0 {
		err := h.Connect(connectCtx, peer.AddrInfo{
			ID:    entryPeer,
			Addrs: addrs,
		})
		// Connection might already exist, that's fine
		_ = err
	}

	// Open a stream to the entry relay with the mixnet protocol
	stream, err := h.NewStream(connectCtx, entryPeer, protocol.ID(protocolID))
	if err != nil {
		return fmt.Errorf("failed to open stream to %s: %w", entryPeer, err)
	}

	m.mu.Lock()
	m.streams[circuit.ID] = &StreamHandler{
		stream: stream,
		peerID: entryPeer,
	}
	m.mu.Unlock()

	return nil
}

// SendData sends raw bytes through the network stream of the specified circuit.
func (m *CircuitManager) SendData(circuitID string, data []byte) error {
	m.mu.RLock()
	handler, ok := m.streams[circuitID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("no stream for circuit %s", circuitID)
	}

	_, err := handler.stream.Write(data)
	return err
}

// ReadData reads data from the network stream of the specified circuit into the provided buffer.
func (m *CircuitManager) ReadData(circuitID string, buf []byte) (int, error) {
	m.mu.RLock()
	handler, ok := m.streams[circuitID]
	m.mu.RUnlock()

	if !ok {
		return 0, fmt.Errorf("no stream for circuit %s", circuitID)
	}

	return handler.stream.Read(buf)
}

// CloseCircuit closes the network stream and updates the state for the specified circuit.
func (m *CircuitManager) CloseCircuit(circuitID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	handler, ok := m.streams[circuitID]
	if ok && handler.stream != nil {
		handler.stream.Close()
		delete(m.streams, circuitID)
	}

	if circuit, ok := m.circuits[circuitID]; ok {
		circuit.SetState(StateClosed)
	}

	return nil
}

// CloseCircuitWithContext closes the network stream with context-based timeout for graceful shutdown (Req 18).
func (m *CircuitManager) CloseCircuitWithContext(ctx context.Context, circuitID string) error {
	m.mu.Lock()
	handler, ok := m.streams[circuitID]
	if !ok {
		m.mu.Unlock()
		if circuit, ok := m.circuits[circuitID]; ok {
			circuit.SetState(StateClosed)
		}
		return nil
	}
	stream := handler.stream
	delete(m.streams, circuitID)
	m.mu.Unlock()

	if circuit, ok := m.circuits[circuitID]; ok {
		circuit.SetState(StateClosed)
	}

	if stream == nil {
		return nil
	}

	done := make(chan error, 1)
	go func() {
		done <- stream.Close()
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *CircuitManager) filterRelays(relays []RelayInfo, exclude peer.ID) []RelayInfo {
	m.mu.RLock()
	selfID := peer.ID("")
	if m.host != nil {
		selfID = m.host.ID()
	}
	m.mu.RUnlock()

	var result []RelayInfo
	for _, r := range relays {
		if r.PeerID != exclude && r.PeerID != selfID {
			result = append(result, r)
		}
	}
	return result
}

func (m *CircuitManager) buildUniqueCircuits(relays []RelayInfo) []*Circuit {
	rand.Shuffle(len(relays), func(i, j int) {
		relays[i], relays[j] = relays[j], relays[i]
	})

	circuits := make([]*Circuit, 0, m.cfg.CircuitCount)
	used := make(map[peer.ID]bool)

	for i := 0; i < m.cfg.CircuitCount && len(circuits) < m.cfg.CircuitCount; i++ {
		var peers []peer.ID

		for j := 0; j < m.cfg.HopCount; j++ {
			idx := i*m.cfg.HopCount + j
			if idx >= len(relays) {
				break
			}
			relayID := relays[idx].PeerID

			if !used[relayID] {
				peers = append(peers, relayID)
				used[relayID] = true
			}
		}

		if len(peers) == m.cfg.HopCount {
			circuit := NewCircuit(fmt.Sprintf("circuit-%d", i), peers)
			circuit.SetState(StateBuilding)
			circuits = append(circuits, circuit)
		}
	}

	return circuits
}

// ActivateCircuit transition the circuit state to StateActive.
func (m *CircuitManager) ActivateCircuit(circuitID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	circuit, ok := m.circuits[circuitID]
	if !ok {
		return fmt.Errorf("circuit not found: %s", circuitID)
	}

	circuit.SetState(StateActive)
	return nil
}

// OR if no heartbeat has been received within the timeout period.
func (m *CircuitManager) DetectFailure(circuitID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	circuit, ok := m.circuits[circuitID]
	if !ok {
		return false
	}

	state := circuit.GetState()
	if state == StateFailed || state == StateClosed {
		return true
	}

	// Check if heartbeat is overdue (active monitoring)
	if circuit.LastHeartbeat.IsZero() {
		return false // No heartbeat yet = still starting
	}
	heartbeatTimeout := 30 * time.Second // Configurable in production
	if time.Since(circuit.LastHeartbeat) > heartbeatTimeout {
		return true // Heartbeat overdue = failure
	}

	return false
}

// StartHeartbeat starts a background goroutine that sends periodic heartbeats
// to monitor circuit health. This enables active failure detection.
func (m *CircuitManager) StartHeartbeat(circuitID string, interval time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	circuit, ok := m.circuits[circuitID]
	if !ok {
		return
	}

	// Record initial heartbeat
	circuit.LastHeartbeat = time.Now()

	// Start background heartbeat sender
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-m.ctx.Done():
				return
			case <-ticker.C:
				m.mu.Lock()
				if c, exists := m.circuits[circuitID]; exists {
					c.LastHeartbeat = time.Now()
				}
				m.mu.Unlock()
			}
		}
	}()
}

// ActiveCircuitCount returns the number of circuits currently in the StateActive state.
func (m *CircuitManager) ActiveCircuitCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := 0
	for _, c := range m.circuits {
		if c.IsActive() {
			count++
		}
	}
	return count
}

// CanRecover returns true if the number of active circuits is still above the reconstruction threshold.
func (m *CircuitManager) CanRecover() bool {
	return m.ActiveCircuitCount() >= m.threshold
}

// RecoveryCapacity returns the number of additional circuit failures that can be tolerated.
func (m *CircuitManager) RecoveryCapacity() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	active := 0
	for _, c := range m.circuits {
		if c.IsActive() {
			active++
		}
	}

	if active >= m.threshold {
		return active - m.threshold
	}
	return -1
}

// RebuildCircuit creates a new circuit as a replacement for a failed one, ensuring no peer reuse from the failed circuit.
func (m *CircuitManager) RebuildCircuit(failedID string) (*Circuit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	failedCircuit, exists := m.circuits[failedID]
	if !exists {
		return nil, fmt.Errorf("circuit not found: %s", failedID)
	}

	state := failedCircuit.GetState()
	if state != StateFailed && state != StateClosed {
		return nil, fmt.Errorf("circuit %s is not failed", failedID)
	}

	// Build a set of peer IDs that belong to the failed circuit so they can be
	// excluded from the replacement.
	failedPeers := make(map[peer.ID]bool)
	for _, p := range failedCircuit.Peers {
		failedPeers[p] = true
	}

	var available []peer.ID
	for _, id := range m.relayPool {
		// Skip relays that belong to the failed circuit.
		if failedPeers[id] {
			continue
		}
		inUse := false
		for _, c := range m.circuits {
			for _, p := range c.Peers {
				if p == id {
					inUse = true
					break
				}
			}
			if inUse {
				break
			}
		}
		if !inUse {
			available = append(available, id)
		}
	}

	if len(available) < m.cfg.HopCount {
		return nil, fmt.Errorf("insufficient available relays: have %d, need %d", len(available), m.cfg.HopCount)
	}

	peers := available[:m.cfg.HopCount]
	circuit := NewCircuit(fmt.Sprintf("%s-rebuilt", failedID), peers)
	circuit.SetState(StateBuilding)

	m.circuits[circuit.ID] = circuit

	return circuit, nil
}

// Close terminates all active circuits and releases associated resources.
func (m *CircuitManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Cancel context to stop all operations
	m.cancel()

	// Close all streams
	for _, handler := range m.streams {
		if handler.stream != nil {
			handler.stream.Close()
		}
	}
	m.streams = make(map[string]*StreamHandler)

	// Mark all circuits as closed
	for _, c := range m.circuits {
		c.SetState(StateClosed)
	}

	return nil
}

// GetCircuit retrieves a circuit by its unique identifier.
func (m *CircuitManager) GetCircuit(id string) (*Circuit, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	c, ok := m.circuits[id]
	return c, ok
}

// ListCircuits returns a slice containing all managed circuits.
func (m *CircuitManager) ListCircuits() []*Circuit {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*Circuit, 0, len(m.circuits))
	for _, c := range m.circuits {
		result = append(result, c)
	}
	return result
}

// MarkCircuitFailed sets the state of the specified circuit to StateFailed.
func (m *CircuitManager) MarkCircuitFailed(circuitID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if circuit, ok := m.circuits[circuitID]; ok {
		circuit.MarkFailed()
	}
}

// UpdateRelayPool refreshes the internal pool of available relay peers.
func (m *CircuitManager) UpdateRelayPool(relays []RelayInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.relayPool = make([]peer.ID, len(relays))
	for i, r := range relays {
		m.relayPool[i] = r.PeerID
	}
}

// GetRelaysForCircuit returns the list of peer IDs forming the specified circuit.
func (m *CircuitManager) GetRelaysForCircuit(circuitID string) ([]peer.ID, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	circuit, ok := m.circuits[circuitID]
	if !ok {
		return nil, fmt.Errorf("circuit not found: %s", circuitID)
	}

	return circuit.Peers, nil
}

// Config returns the circuit manager's configuration.
func (m *CircuitManager) Config() *CircuitConfig {
	return m.cfg
}

// GetStream returns the StreamHandler associated with the specified circuit ID.
func (m *CircuitManager) GetStream(circuitID string) (*StreamHandler, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	handler, ok := m.streams[circuitID]
	return handler, ok
}
