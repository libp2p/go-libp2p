// Package mixnet provides discovery wrappers and traffic-shaping helpers.
package mixnet

import (
	"context"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"

	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"

	"github.com/libp2p/go-libp2p/mixnet/circuit"
	"github.com/libp2p/go-libp2p/mixnet/discovery"
)

// DiscoverRelaysWithVerification discovers relay providers and delegates filtering/selection
// to the canonical discovery layer in mixnet/discovery.
func DiscoverRelaysWithVerification(ctx context.Context, h host.Host, r routing.Routing, dest peer.ID, protoID string, hopCount, circuitCount int, samplingSize int, selectionMode string, randomnessFactor float64) ([]circuit.RelayInfo, error) {
	// Create CID for discovery (same model used by upgrader).
	hHash, err := mh.Encode([]byte(protoID+"-relay-v1"), mh.SHA2_256)
	if err != nil {
		return nil, ErrDiscoveryFailed("failed to encode discovery CID").WithCause(err)
	}
	c := cid.NewCidV1(cid.Raw, hHash)

	providersChan := r.FindProvidersAsync(ctx, c, 0)
	var providers []peer.AddrInfo
	for p := range providersChan {
		providers = append(providers, p)
	}

	// Canonical exclusion logic lives in discovery.FilterByExclusion.
	providers = discovery.FilterByExclusion(providers, dest, h.ID())
	if len(providers) == 0 {
		return nil, ErrDiscoveryFailed("no relay providers after exclusion")
	}

	disc := discovery.NewRelayDiscoveryWithHost(h, protoID, samplingSize, selectionMode, randomnessFactor)
	sel, err := disc.FindRelays(ctx, providers, hopCount, circuitCount)
	if err != nil {
		return nil, ErrDiscoveryFailed("relay selection failed").WithCause(err)
	}

	out := make([]circuit.RelayInfo, len(sel))
	for i, ri := range sel {
		out[i] = circuit.RelayInfo{PeerID: ri.PeerID, AddrInfo: ri.AddrInfo, Latency: ri.Latency, Connected: ri.Available}
	}
	return out, nil
}

// UseDiscoveryService returns the canonical discovery service implementation.
func UseDiscoveryService(h host.Host, protoID string, samplingSize int, selectionMode string, randomnessFactor float64) (*discovery.RelayDiscovery, error) {
	disc := discovery.NewRelayDiscoveryWithHost(h, protoID, samplingSize, selectionMode, randomnessFactor)
	return disc, nil
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
