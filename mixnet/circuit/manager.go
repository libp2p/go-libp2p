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

// FailureCallback is a function type for handling circuit failures.
type FailureCallback func(circuitID string)

// RelayInfo holds metadata and performance information for a potential relay node.
type RelayInfo struct {
	PeerID    peer.ID
	AddrInfo  peer.AddrInfo
	Latency   time.Duration
	Connected bool
}

type CircuitConfig struct {
	HopCount      int
	CircuitCount  int
	StreamTimeout time.Duration
}

type StreamHandler struct {
	stream network.Stream
	peerID peer.ID
}

func (h *StreamHandler) Stream() network.Stream {
	return h.stream
}

type CircuitManager struct {
	cfg       *CircuitConfig
	circuits  map[string]*Circuit
	relayPool []peer.ID
	threshold int
	host      host.Host
	streams   map[string]*StreamHandler
	mu        sync.RWMutex
	wg        sync.WaitGroup
	ctx       context.Context
	cancel    context.CancelFunc
}

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

func (m *CircuitManager) SetHost(h host.Host) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.host = h
}

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

func (m *CircuitManager) BuildCircuit() (*Circuit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.relayPool) < m.cfg.HopCount {
		return nil, fmt.Errorf("insufficient relays in pool")
	}

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

	connectCtx, cancel := context.WithTimeout(m.ctx, m.cfg.StreamTimeout)
	defer cancel()

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
		_ = err
	}

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

func (m *CircuitManager) ReadData(circuitID string, buf []byte) (int, error) {
	m.mu.RLock()
	handler, ok := m.streams[circuitID]
	m.mu.RUnlock()

	if !ok {
		return 0, fmt.Errorf("no stream for circuit %s", circuitID)
	}

	return handler.stream.Read(buf)
}

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

	if circuit.LastHeartbeat.IsZero() {
		return false
	}
	heartbeatTimeout := 30 * time.Second
	if time.Since(circuit.LastHeartbeat) > heartbeatTimeout {
		return true
	}

	return false
}

func (m *CircuitManager) StartHeartbeat(circuitID string, interval time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	circuit, ok := m.circuits[circuitID]
	if !ok {
		return
	}

	circuit.LastHeartbeat = time.Now()

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

func (m *CircuitManager) CanRecover() bool {
	return m.ActiveCircuitCount() >= m.threshold
}

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

	failedPeers := make(map[peer.ID]bool)
	for _, p := range failedCircuit.Peers {
		failedPeers[p] = true
	}

	var available []peer.ID
	for _, id := range m.relayPool {
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

func (m *CircuitManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cancel()

	for _, handler := range m.streams {
		if handler.stream != nil {
			handler.stream.Close()
		}
	}
	m.streams = make(map[string]*StreamHandler)

	for _, c := range m.circuits {
		c.SetState(StateClosed)
	}

	return nil
}

func (m *CircuitManager) GetCircuit(id string) (*Circuit, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	c, ok := m.circuits[id]
	return c, ok
}

func (m *CircuitManager) ListCircuits() []*Circuit {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*Circuit, 0, len(m.circuits))
	for _, c := range m.circuits {
		result = append(result, c)
	}
	return result
}

func (m *CircuitManager) MarkCircuitFailed(circuitID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if circuit, ok := m.circuits[circuitID]; ok {
		circuit.MarkFailed()
	}
}

func (m *CircuitManager) UpdateRelayPool(relays []RelayInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.relayPool = make([]peer.ID, len(relays))
	for i, r := range relays {
		m.relayPool[i] = r.PeerID
	}
}

func (m *CircuitManager) GetRelaysForCircuit(circuitID string) ([]peer.ID, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	circuit, ok := m.circuits[circuitID]
	if !ok {
		return nil, fmt.Errorf("circuit not found: %s", circuitID)
	}

	return circuit.Peers, nil
}

func (m *CircuitManager) Config() *CircuitConfig {
	return m.cfg
}

func (m *CircuitManager) GetStream(circuitID string) (*StreamHandler, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	handler, ok := m.streams[circuitID]
	return handler, ok
}
