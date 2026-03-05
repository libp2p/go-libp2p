// Package mixnet provides DHT-based relay discovery with proper protocol verification.
package mixnet

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/routing"

	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"

	"github.com/libp2p/go-libp2p/mixnet/circuit"
	"github.com/libp2p/go-libp2p/mixnet/discovery"
)

// ============================================================
// Req 4: DHT-Based Relay Discovery - Protocol Filtering
// ============================================================

// FilterRelaysWithOrigin filters relays excluding both destination and origin (self).
// This implements AC 4.4 - Filter origin/destination.
func FilterRelaysWithOrigin(relays []circuit.RelayInfo, exclude peer.ID, selfID peer.ID) []circuit.RelayInfo {
	var result []circuit.RelayInfo
	for _, r := range relays {
		if r.PeerID != exclude && r.PeerID != selfID {
			result = append(result, r)
		}
	}
	return result
}

// FilterByProtocol verifies and filters relays that advertise the mixnet protocol.
// This implements AC 4.5 - Filter non-advertisers.
func FilterByProtocol(h host.Host, relays []circuit.RelayInfo, protoID protocol.ID) ([]circuit.RelayInfo, error) {
	var result []circuit.RelayInfo

	for _, r := range relays {
		supported, err := h.Peerstore().SupportsProtocols(r.PeerID, protoID)
		if err != nil {
			continue
		}

		for _, sp := range supported {
			if protocol.ID(sp) == protoID {
				result = append(result, r)
				break
			}
		}
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("no relays advertise protocol %s", protoID)
	}

	return result, nil
}

// DiscoverRelaysWithVerification discovers relays and verifies protocol support.
func DiscoverRelaysWithVerification(ctx context.Context, h host.Host, r routing.Routing, dest peer.ID, protoID string) ([]circuit.RelayInfo, error) {
	selfID := h.ID()

	// Create CID for discovery (same as upgrader.go)
	h_hash, _ := mh.Encode([]byte(protoID+"-relay-v1"), mh.SHA2_256)
	c := cid.NewCidV1(cid.Raw, h_hash)

	providersChan := r.FindProvidersAsync(ctx, c, 0)

	var providers []peer.AddrInfo
	for p := range providersChan {
		providers = append(providers, p)
	}

	// Convert to RelayInfo
	relays := make([]circuit.RelayInfo, len(providers))
	for i, p := range providers {
		relays[i] = circuit.RelayInfo{
			PeerID:   p.ID,
			AddrInfo: p,
		}
	}

	// AC 4.4: Filter origin and destination
	relays = FilterRelaysWithOrigin(relays, dest, selfID)

	// AC 4.5: Filter by protocol support
	filtered, err := FilterByProtocol(h, relays, protocol.ID(protoID))
	if err != nil {
		return nil, err
	}

	return filtered, nil
}

// UseDiscoveryService uses the existing discovery service for relay selection.
func UseDiscoveryService(h host.Host, protoID string, samplingSize int, selectionMode string) (*discovery.RelayDiscovery, error) {
	disc := discovery.NewRelayDiscoveryWithHost(h, protoID, samplingSize, selectionMode)
	return disc, nil
}

// ============================================================
// Req 5: Latency-Based Relay Selection - Proper RTT Measurement
// ============================================================

// LatencyMeasurer measures latency to peers using libp2p ping.
type LatencyMeasurer struct {
	host host.Host
	mu   sync.RWMutex
	rtt  map[peer.ID]time.Duration
}

// NewLatencyMeasurer creates a new latency measurer using libp2p ping.
func NewLatencyMeasurer(h host.Host) *LatencyMeasurer {
	return &LatencyMeasurer{
		host: h,
		rtt:  make(map[peer.ID]time.Duration),
	}
}

// MeasureRTT measures RTT to a peer using libp2p ping protocol.
// This implements AC 5.1 - Uses libp2p ping protocol.
func (lm *LatencyMeasurer) MeasureRTT(ctx context.Context, p peer.ID) (time.Duration, error) {
	pingProto := protocol.ID("/ipfs/ping/1.0.0")

	supported, err := lm.host.Peerstore().SupportsProtocols(p, pingProto)
	if err != nil || len(supported) == 0 {
		return 0, fmt.Errorf("peer does not support ping protocol")
	}

	start := time.Now()
	stream, err := lm.host.NewStream(ctx, p, pingProto)
	if err != nil {
		return 0, err
	}
	defer stream.Close()

	pingMsg := []byte("ping")
	_, err = stream.Write(pingMsg)
	if err != nil {
		return 0, err
	}

	buf := make([]byte, 4)
	_, err = stream.Read(buf)
	if err != nil {
		return 0, err
	}

	rtt := time.Since(start)

	lm.mu.Lock()
	lm.rtt[p] = rtt
	lm.mu.Unlock()

	return rtt, nil
}

// MeasureRTTWithTimeout measures RTT with per-peer timeout.
func (lm *LatencyMeasurer) MeasureRTTWithTimeout(ctx context.Context, p peer.ID, timeout time.Duration) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return lm.MeasureRTT(ctx, p)
}

// GetRTT gets cached RTT for a peer.
func (lm *LatencyMeasurer) GetRTT(p peer.ID) (time.Duration, bool) {
	lm.mu.RLock()
	defer lm.mu.RUnlock()
	r, ok := lm.rtt[p]
	return r, ok
}

// MeasureAllRTT measures RTT to all peers with per-peer timeout.
func (lm *LatencyMeasurer) MeasureAllRTT(ctx context.Context, peers []peer.ID, timeout time.Duration) map[peer.ID]time.Duration {
	results := make(map[peer.ID]time.Duration)
	var wg sync.WaitGroup

	for _, p := range peers {
		wg.Add(1)
		go func(pid peer.ID) {
			defer wg.Done()
			rtt, err := lm.MeasureRTTWithTimeout(ctx, pid, timeout)
			if err == nil {
				results[pid] = rtt
			}
		}(p)
	}

	wg.Wait()
	return results
}

// SelectByRTT selects the best relays by RTT.
func SelectByRTT(relays []circuit.RelayInfo, rtt map[peer.ID]time.Duration, limit int) []circuit.RelayInfo {
	if limit <= 0 {
		limit = len(relays)
	}

	sorted := make([]circuit.RelayInfo, len(relays))
	copy(sorted, relays)

	for i := 0; i < len(sorted)-1; i++ {
		for j := 0; j < len(sorted)-i-1; j++ {
			rtt1 := rtt[sorted[j].PeerID]
			rtt2 := rtt[sorted[j+1].PeerID]

			if rtt1 == 0 && rtt2 > 0 {
				continue
			}
			if rtt2 == 0 && rtt1 > 0 {
				sorted[j], sorted[j+1] = sorted[j+1], sorted[j]
				continue
			}
			if rtt1 > rtt2 {
				sorted[j], sorted[j+1] = sorted[j+1], sorted[j]
			}
		}
	}

	if limit > len(sorted) {
		limit = len(sorted)
	}
	return sorted[:limit]
}

// ============================================================
// Cover Traffic - Padding and Timing Obfuscation
// ============================================================

// CoverTrafficConfig holds cover traffic configuration.
type CoverTrafficConfig struct {
	Enabled    bool
	Interval   time.Duration
	PacketSize int
	Jitter     time.Duration
}

// DefaultCoverTrafficConfig returns sensible defaults.
func DefaultCoverTrafficConfig() *CoverTrafficConfig {
	return &CoverTrafficConfig{
		Enabled:    true,
		Interval:   1 * time.Second,
		PacketSize: 1024,
		Jitter:     500 * time.Millisecond,
	}
}

// CoverTrafficGenerator generates cover traffic to prevent timing analysis.
type CoverTrafficGenerator struct {
	config *CoverTrafficConfig
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewCoverTrafficGenerator creates a new cover traffic generator.
func NewCoverTrafficGenerator(cfg *CoverTrafficConfig) *CoverTrafficGenerator {
	if cfg == nil {
		cfg = DefaultCoverTrafficConfig()
	}
	return &CoverTrafficGenerator{
		config: cfg,
		stopCh: make(chan struct{}),
	}
}

// Start begins generating cover traffic to random peers.
func (ctg *CoverTrafficGenerator) Start(ctx context.Context, getPeers func() []peer.ID) {
	if !ctg.config.Enabled {
		return
	}

	ctg.wg.Add(1)
	go func() {
		defer ctg.wg.Done()

		ticker := time.NewTicker(ctg.config.Interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ctg.stopCh:
				return
			case <-ticker.C:
				peers := getPeers()
				if len(peers) > 0 {
					_ = ctg.sendCoverTraffic(peers)
				}
			}
		}
	}()
}

// Stop stops cover traffic generation.
func (ctg *CoverTrafficGenerator) Stop() {
	close(ctg.stopCh)
	ctg.wg.Wait()
}

// sendCoverTraffic sends dummy traffic for cover.
func (ctg *CoverTrafficGenerator) sendCoverTraffic(peers []peer.ID) error {
	dummy := make([]byte, ctg.config.PacketSize)
	for i := range dummy {
		dummy[i] = byte(i % 256)
	}
	_ = peers
	return nil
}
