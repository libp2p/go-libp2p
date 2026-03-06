// Package mixnet provides relay node resource management and rate limiting.
package mixnet

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
)

// ============================================================
// Req 20: Relay Node Resource Limits
// ============================================================

// ResourceConfig holds resource limit configuration.
type ResourceConfig struct {
	// MaxConcurrentCircuits is the maximum number of concurrent circuits (AC 20.1)
	MaxConcurrentCircuits int
	// MaxBandwidthBytesPerSec is the maximum bandwidth in bytes/sec (AC 20.2)
	MaxBandwidthBytesPerSec int64
	// MaxConnectionsPerPeer limits connections from a single peer
	MaxConnectionsPerPeer int
	// CircuitTimeout is how long a circuit can be idle before cleanup
	CircuitTimeout time.Duration
	// EnableBackpressure enables backpressure when limits reached (AC 20.4)
	EnableBackpressure bool
}

// DefaultResourceConfig returns sensible defaults.
func DefaultResourceConfig() *ResourceConfig {
	return &ResourceConfig{
		MaxConcurrentCircuits:   100,
		MaxBandwidthBytesPerSec: 1024 * 1024, // 1 MB/s
		MaxConnectionsPerPeer:   10,
		CircuitTimeout:          30 * time.Minute,
		EnableBackpressure:      true,
	}
}

// ResourceManager manages relay node resources and enforces limits.
type ResourceManager struct {
	config *ResourceConfig
	libp2p network.ResourceManager

	// Circuit tracking (AC 20.1, 20.3)
	mu             sync.RWMutex
	activeCircuits map[string]*ResourceCircuit
	circuitCount   int64

	// Bandwidth tracking (AC 20.2)
	bandwidthMu        sync.Mutex
	currentBandwidth   int64
	bandwidthPerSec    int64
	lastBandwidthCheck time.Time

	// Connection tracking per peer (AC 20.3)
	peerConnections map[peer.ID]int

	// Backpressure signaling
	backpressureCh chan struct{}
	stopCh         chan struct{}
}

// ResourceCircuit holds circuit resource info.
type ResourceCircuit struct {
	CircuitID  string
	PeerID     peer.ID
	CreatedAt  time.Time
	LastActive time.Time
	BytesIn    int64
	BytesOut   int64
}

// NewResourceManager creates a new resource manager.
func NewResourceManager(cfg *ResourceConfig) *ResourceManager {
	if cfg == nil {
		cfg = DefaultResourceConfig()
	}

	return &ResourceManager{
		config:          cfg,
		activeCircuits:  make(map[string]*ResourceCircuit),
		peerConnections: make(map[peer.ID]int),
		backpressureCh:  make(chan struct{}, 1),
		stopCh:          make(chan struct{}),
	}
}

// NewLibp2pResourceManager creates a resource manager that is backed by libp2p rcmgr.
func NewLibp2pResourceManager(h host.Host, cfg *ResourceConfig) *ResourceManager {
	rm := NewResourceManager(cfg)
	if h != nil && h.Network() != nil {
		rm.libp2p = h.Network().ResourceManager()
	}
	return rm
}

// UsesLibp2p reports whether this manager is backed by libp2p rcmgr.
func (rm *ResourceManager) UsesLibp2p() bool {
	return rm != nil && rm.libp2p != nil
}

// AdmitOutboundStream asks libp2p rcmgr for an outbound stream scope and tags it.
// The returned function must be called to release the scope.
func (rm *ResourceManager) AdmitOutboundStream(p peer.ID, protoID protocol.ID, service string) (func(), error) {
	if rm == nil || rm.libp2p == nil {
		return func() {}, nil
	}
	scope, err := rm.libp2p.OpenStream(p, network.DirOutbound)
	if err != nil {
		return nil, err
	}
	if err := scope.SetProtocol(protoID); err != nil {
		scope.Done()
		return nil, err
	}
	if service != "" {
		_ = scope.SetService(service)
	}
	return scope.Done, nil
}

// ReserveInboundMemory reserves memory against a stream scope in libp2p rcmgr.
// The returned function releases the reservation.
func (rm *ResourceManager) ReserveInboundMemory(scope network.StreamScope, bytes int) (func(), error) {
	if rm == nil || rm.libp2p == nil || scope == nil || bytes <= 0 {
		return func() {}, nil
	}
	if err := scope.ReserveMemory(bytes, network.ReservationPriorityMedium); err != nil {
		return nil, err
	}
	return func() { scope.ReleaseMemory(bytes) }, nil
}

// CanAcceptCircuit checks if we can accept a new circuit (AC 20.3).
// Returns error if at limit.
func (rm *ResourceManager) CanAcceptCircuit() error {
	count := atomic.LoadInt64(&rm.circuitCount)
	max := int64(rm.config.MaxConcurrentCircuits)

	if max > 0 && count >= max {
		return fmt.Errorf("at maximum circuit capacity: %d/%d", count, max)
	}
	return nil
}

// RegisterCircuit registers a new circuit and returns its ID (AC 20.3).
func (rm *ResourceManager) RegisterCircuit(circuitID string, peerID peer.ID) error {
	if err := rm.CanAcceptCircuit(); err != nil {
		return err
	}

	// Check peer connection limit (AC 20.3)
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if rm.config.MaxConnectionsPerPeer > 0 {
		if rm.peerConnections[peerID] >= rm.config.MaxConnectionsPerPeer {
			return fmt.Errorf("peer %s at connection limit: %d", peerID, rm.peerConnections[peerID])
		}
		rm.peerConnections[peerID]++
	}

	// Register circuit
	rm.activeCircuits[circuitID] = &ResourceCircuit{
		CircuitID:  circuitID,
		PeerID:     peerID,
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
	}
	atomic.AddInt64(&rm.circuitCount, 1)

	return nil
}

// UnregisterCircuit removes a circuit (AC 20.3).
func (rm *ResourceManager) UnregisterCircuit(circuitID string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if rc, ok := rm.activeCircuits[circuitID]; ok {
		delete(rm.activeCircuits, circuitID)
		atomic.AddInt64(&rm.circuitCount, -1)

		// Decrement peer connection count
		if rm.config.MaxConnectionsPerPeer > 0 {
			if rm.peerConnections[rc.PeerID] > 0 {
				rm.peerConnections[rc.PeerID]--
			}
		}
	}
}

// RecordBandwidth records bandwidth usage (AC 20.2).
func (rm *ResourceManager) RecordBandwidth(bytes int64, direction string) {
	rm.bandwidthMu.Lock()
	defer rm.bandwidthMu.Unlock()

	rm.currentBandwidth += bytes
	now := time.Now()

	// Reset per-second tracking
	if now.Sub(rm.lastBandwidthCheck) >= time.Second {
		rm.bandwidthPerSec = rm.currentBandwidth
		rm.currentBandwidth = 0
		rm.lastBandwidthCheck = now

		// Check if we're over bandwidth limit (AC 20.4 - Backpressure)
		if rm.config.EnableBackpressure && rm.bandwidthPerSec > rm.config.MaxBandwidthBytesPerSec {
			select {
			case rm.backpressureCh <- struct{}{}:
			default:
				// Channel full, skip
			}
		}
	}
}

// CanSend checks if we can send more data based on bandwidth limits (AC 20.4).
func (rm *ResourceManager) CanSend(bytes int64) bool {
	if rm.config.MaxBandwidthBytesPerSec <= 0 {
		return true
	}

	rm.bandwidthMu.Lock()
	defer rm.bandwidthMu.Unlock()

	// Check if adding these bytes would exceed limit
	projected := rm.currentBandwidth + bytes
	if projected > rm.config.MaxBandwidthBytesPerSec {
		return false
	}

	return true
}

// WaitForBandwidth waits until bandwidth is available (AC 20.4 - Backpressure).
func (rm *ResourceManager) WaitForBandwidth(ctx context.Context, bytes int64) error {
	if rm.config.MaxBandwidthBytesPerSec <= 0 {
		return nil
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		if rm.CanSend(bytes) {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			continue
		}
	}
}

// BackpressureChan returns a channel that signals when backpressure is needed.
func (rm *ResourceManager) BackpressureChan() <-chan struct{} {
	return rm.backpressureCh
}

// ActiveCircuitCount returns the current number of active circuits.
func (rm *ResourceManager) ActiveCircuitCount() int {
	return int(atomic.LoadInt64(&rm.circuitCount))
}

// SetActiveCircuitCount sets the observed active circuit count.
// Used when relay layer tracks active circuits directly.
func (rm *ResourceManager) SetActiveCircuitCount(n int) {
	if rm == nil {
		return
	}
	if n < 0 {
		n = 0
	}
	atomic.StoreInt64(&rm.circuitCount, int64(n))
}

// BandwidthPerSec returns current bandwidth usage per second.
func (rm *ResourceManager) BandwidthPerSec() int64 {
	rm.bandwidthMu.Lock()
	defer rm.bandwidthMu.Unlock()
	return rm.bandwidthPerSec
}

// IsAtCapacity returns true if at circuit capacity.
func (rm *ResourceManager) IsAtCapacity() bool {
	count := atomic.LoadInt64(&rm.circuitCount)
	max := int64(rm.config.MaxConcurrentCircuits)
	return max > 0 && count >= max
}

// CleanupIdleCircuits removes circuits that have been idle too long.
func (rm *ResourceManager) CleanupIdleCircuits() {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	timeout := rm.config.CircuitTimeout
	now := time.Now()

	for id, rc := range rm.activeCircuits {
		if now.Sub(rc.LastActive) > timeout {
			delete(rm.activeCircuits, id)
			atomic.AddInt64(&rm.circuitCount, -1)

			if rm.config.MaxConnectionsPerPeer > 0 {
				if rm.peerConnections[rc.PeerID] > 0 {
					rm.peerConnections[rc.PeerID]--
				}
			}
		}
	}
}

// UpdateActivity updates the last active time for a circuit.
func (rm *ResourceManager) UpdateActivity(circuitID string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if rc, ok := rm.activeCircuits[circuitID]; ok {
		rc.LastActive = time.Now()
	}
}

// Config returns the resource manager configuration.
func (rm *ResourceManager) Config() *ResourceConfig {
	if rm == nil {
		return nil
	}
	return rm.config
}

// UtilizationPercent returns a coarse 0-100 utilization based on max of
// circuit occupancy and bandwidth occupancy.
func (rm *ResourceManager) UtilizationPercent() float64 {
	if rm == nil || rm.config == nil {
		return 0
	}

	circuitUtil := 0.0
	if rm.config.MaxConcurrentCircuits > 0 {
		circuitUtil = float64(rm.ActiveCircuitCount()) / float64(rm.config.MaxConcurrentCircuits) * 100
	}

	bwUtil := 0.0
	if rm.config.MaxBandwidthBytesPerSec > 0 {
		bwUtil = float64(rm.BandwidthPerSec()) / float64(rm.config.MaxBandwidthBytesPerSec) * 100
	}

	if bwUtil > circuitUtil {
		return bwUtil
	}
	return circuitUtil
}

// StartCleanup starts the background cleanup goroutine.
func (rm *ResourceManager) StartCleanup(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-rm.stopCh:
				return
			case <-ticker.C:
				rm.CleanupIdleCircuits()
			}
		}
	}()
}

// Stop stops the resource manager.
func (rm *ResourceManager) Stop() {
	close(rm.stopCh)
}

// ============================================================
// Integration with existing Mixnet
// ============================================================

// MixnetWithResources extends Mixnet with resource management.
type MixnetWithResources struct {
	*Mixnet
	resourceMgr *ResourceManager
}

// NewMixnetWithResources creates a mixnet with resource management.
func NewMixnetWithResources(cfg *MixnetConfig, h host.Host, r routing.Routing, resourceCfg *ResourceConfig) (*MixnetWithResources, error) {
	mix, err := NewMixnet(cfg, h, r)
	if err != nil {
		return nil, err
	}

	resourceMgr := NewLibp2pResourceManager(h, resourceCfg)

	// Default to libp2p rcmgr-driven resource enforcement at the relay layer.
	if mix.relayHandler != nil {
		mix.relayHandler.EnableLibp2pResourceManager(true)
		mix.relayHandler.SetResourceServiceName("mixnet-relay")
	}

	return &MixnetWithResources{
		Mixnet:      mix,
		resourceMgr: resourceMgr,
	}, nil
}

// ResourceManager returns the resource manager.
func (m *MixnetWithResources) ResourceManager() *ResourceManager {
	return m.resourceMgr
}

// CloseWithResources shuts down and cleans up resources.
func (m *MixnetWithResources) CloseWithResources() error {
	// Stop resource manager cleanup
	m.resourceMgr.Stop()

	// Erase keys
	m.pipeline.Encrypter().SecureErase()

	// Close circuits
	return m.CircuitManager().Close()
}
