// Package discovery handles the discovery and selection of mixnet relay nodes.
package discovery

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ping "github.com/libp2p/go-libp2p/p2p/protocol/ping"
)

// RelayDiscovery provides mechanisms for discovering and selecting mixnet relays.
type RelayDiscovery struct {
	protocolID    string
	samplingSize  int
	selectionMode string // "rtt", "random", "hybrid"
	host          host.Host
	pingService   *ping.PingService
}

// RelayInfo contains information about a candidate relay node discovered in the network.
type RelayInfo struct {
	// PeerID is the unique ID of the relay peer.
	PeerID    peer.ID
	// AddrInfo contains the addresses of the relay peer.
	AddrInfo  peer.AddrInfo
	// Latency is the measured RTT to the relay.
	Latency   time.Duration
	// Available indicates if the relay is currently considered reachable.
	Available bool
}

// NewRelayDiscovery creates a new RelayDiscovery instance with the specified parameters.
func NewRelayDiscovery(protocolID string, samplingSize int, selectionMode string) *RelayDiscovery {
	return &RelayDiscovery{
		protocolID:    protocolID,
		samplingSize:  samplingSize,
		selectionMode: selectionMode,
	}
}

// NewRelayDiscoveryWithHost creates a RelayDiscovery instance that uses a libp2p host for RTT measurements.
func NewRelayDiscoveryWithHost(h host.Host, protocolID string, samplingSize int, selectionMode string) *RelayDiscovery {
	ps := ping.NewPingService(h)
	return &RelayDiscovery{
		protocolID:    protocolID,
		samplingSize:  samplingSize,
		selectionMode: selectionMode,
		host:          h,
		pingService:   ps,
	}
}

// SelectRelays chooses a set of relays from the provided candidates based on the selection mode.
func (r *RelayDiscovery) SelectRelays(ctx context.Context, candidates []RelayInfo) ([]RelayInfo, error) {
	// For now, simple RTT selection
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Latency < candidates[j].Latency
	})
	return candidates, nil
}

// FindRelays discovers potential relay nodes and selects them based on selection mode.
func (r *RelayDiscovery) FindRelays(ctx context.Context, peers []peer.AddrInfo, hopCount, circuitCount int) ([]RelayInfo, error) {
	filtered := r.filterPeers(peers)
	required := hopCount * circuitCount
	if len(filtered) < required {
		return nil, fmt.Errorf("insufficient relay peers: have %d, need %d", len(filtered), required)
	}

	switch r.selectionMode {
	case "random":
		return r.selectRandom(filtered, required)
	case "hybrid":
		return r.selectHybrid(ctx, filtered, required, hopCount, circuitCount, 0.3)
	case "rtt":
		fallthrough
	default:
		return r.selectByRTT(ctx, filtered, required)
	}
}

func (r *RelayDiscovery) filterPeers(peers []peer.AddrInfo) []peer.AddrInfo {
	var result []peer.AddrInfo
	for _, p := range peers {
		// Check protocol support if host is available
		if r.host != nil {
			supported, err := r.host.Peerstore().SupportsProtocols(p.ID, protocol.ID(r.protocolID))
			if err != nil || len(supported) == 0 {
				continue // Skip peers without mixnet protocol
			}
		}
		if len(p.Addrs) > 0 {
			result = append(result, p)
		}
	}
	return result
}

// filterByProtocol filters peers that advertise the mixnet protocol.
// This is a SECURITY fix - prevents selecting malicious non-mixnet peers.
func (r *RelayDiscovery) filterByProtocol(peers []peer.AddrInfo) ([]peer.AddrInfo, error) {
	if r.host == nil {
		return peers, nil // Can't verify without host
	}

	var result []peer.AddrInfo
	for _, p := range peers {
		supported, err := r.host.Peerstore().SupportsProtocols(p.ID, protocol.ID(r.protocolID))
		if err != nil || len(supported) == 0 {
			continue
		}
		result = append(result, p)
	}
	return result, nil
}

func (r *RelayDiscovery) selectRandom(peers []peer.AddrInfo, count int) ([]RelayInfo, error) {
	if len(peers) < count {
		return nil, fmt.Errorf("insufficient peers: have %d, need %d", len(peers), count)
	}

	shuffled := make([]peer.AddrInfo, len(peers))
	copy(shuffled, peers)
	rand.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})

	result := make([]RelayInfo, count)
	for i := 0; i < count; i++ {
		result[i] = RelayInfo{
			PeerID:    shuffled[i].ID,
			AddrInfo:  shuffled[i],
			Available: true,
		}
	}
	return result, nil
}

func (r *RelayDiscovery) selectByRTT(ctx context.Context, peers []peer.AddrInfo, count int) ([]RelayInfo, error) {
	sampled := r.sampleFromPool(peers)
	latencies, err := r.measureLatencies(ctx, sampled)
	if err != nil {
		return nil, err
	}

	sort.Slice(sampled, func(i, j int) bool {
		li := latencies[sampled[i].ID]
		lj := latencies[sampled[j].ID]
		return li < lj
	})

	result := make([]RelayInfo, 0, count)
	used := make(map[peer.ID]bool)

	for _, p := range sampled {
		if len(result) >= count {
			break
		}
		if used[p.ID] {
			continue
		}
		result = append(result, RelayInfo{
			PeerID:   p.ID,
			AddrInfo: p,
			Latency:  latencies[p.ID],
		})
		used[p.ID] = true
	}

	if len(result) < count {
		return nil, fmt.Errorf("could not select enough relays: have %d, need %d", len(result), count)
	}
	return result, nil
}

func (r *RelayDiscovery) selectHybrid(ctx context.Context, peers []peer.AddrInfo, required, hopCount, circuitCount int, randomnessFactor float64) ([]RelayInfo, error) {
	sampleSize := r.samplingSize
	if sampleSize < required {
		sampleSize = required * 2
	}
	if sampleSize > len(peers) {
		sampleSize = len(peers)
	}

	sampled := r.randomSample(peers, sampleSize)
	latencies, err := r.measureLatencies(ctx, sampled)
	if err != nil {
		return nil, err
	}

	relays := r.buildCircuitsWithWeights(sampled, latencies, circuitCount, hopCount, randomnessFactor)
	if len(relays) < required {
		return nil, fmt.Errorf("could not select enough relays")
	}
	return relays, nil
}

func (r *RelayDiscovery) sampleFromPool(peers []peer.AddrInfo) []peer.AddrInfo {
	if r.samplingSize == 0 || len(peers) <= r.samplingSize {
		return peers
	}
	return r.randomSample(peers, r.samplingSize)
}

func (r *RelayDiscovery) randomSample(peers []peer.AddrInfo, k int) []peer.AddrInfo {
	if k >= len(peers) {
		result := make([]peer.AddrInfo, len(peers))
		copy(result, peers)
		return result
	}
	shuffled := make([]peer.AddrInfo, len(peers))
	copy(shuffled, peers)
	rand.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})
	return shuffled[:k]
}

// measureLatencies measures RTT to all provided peers.
func (r *RelayDiscovery) measureLatencies(ctx context.Context, peers []peer.AddrInfo) (map[peer.ID]time.Duration, error) {
	result := make(map[peer.ID]time.Duration)

	if r.pingService == nil {
		// No host available: assign a uniform default so callers get a valid map.
		for _, p := range peers {
			result[p.ID] = 100 * time.Millisecond
		}
		return result, nil
	}

	type resultChan struct {
		peerID  peer.ID
		latency time.Duration
		err     error
	}

	rc := make(chan resultChan, len(peers))

	for _, p := range peers {
		go func(addrInfo peer.AddrInfo) {
			latency, err := r.measureRTTToPeer(ctx, addrInfo)
			rc <- resultChan{addrInfo.ID, latency, err}
		}(p)
	}

	for i := 0; i < len(peers); i++ {
		select {
		case <-ctx.Done():
			return result, fmt.Errorf("context cancelled during latency measurement")
		case res := <-rc:
			if res.err == nil {
				result[res.peerID] = res.latency
			}
		}
	}

	return result, nil
}

// measureRTTToPeer measures round-trip time to a peer using the libp2p ping protocol.
func (r *RelayDiscovery) measureRTTToPeer(ctx context.Context, addrInfo peer.AddrInfo) (time.Duration, error) {
	if r.pingService == nil {
		return 0, fmt.Errorf("ping service not configured")
	}

	// Ensure we are connected before pinging.
	if r.host.Network().Connectedness(addrInfo.ID) != network.Connected {
		connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := r.host.Connect(connectCtx, addrInfo); err != nil {
			return 0, fmt.Errorf("failed to connect to peer %s: %w", addrInfo.ID, err)
		}
	}

	// Send a single ping and return its RTT.
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resCh := r.pingService.Ping(pingCtx, addrInfo.ID)
	select {
	case <-pingCtx.Done():
		return 0, fmt.Errorf("ping timeout for peer %s", addrInfo.ID)
	case res, ok := <-resCh:
		if !ok {
			return 0, fmt.Errorf("ping channel closed for peer %s", addrInfo.ID)
		}
		if res.Error != nil {
			return 0, fmt.Errorf("ping error for peer %s: %w", addrInfo.ID, res.Error)
		}
		return res.RTT, nil
	}
}

type weightedPeer struct {
	peer   peer.AddrInfo
	weight float64
}

func (r *RelayDiscovery) buildCircuitsWithWeights(peers []peer.AddrInfo, latencies map[peer.ID]time.Duration, circuitCount, hopCount int, randomnessFactor float64) []RelayInfo {
	var weightedPeers []weightedPeer
	for _, p := range peers {
		lat := latencies[p.ID]
		if lat == 0 {
			lat = 100 * time.Millisecond
		}
		weight := 1.0 / (float64(lat.Milliseconds()) + 1)
		weightedPeers = append(weightedPeers, weightedPeer{p, weight})
	}

	var result []RelayInfo
	used := make(map[peer.ID]bool)

	for circuit := 0; circuit < circuitCount; circuit++ {
		for hop := 0; hop < hopCount; hop++ {
			var available []weightedPeer
			for _, wp := range weightedPeers {
				if !used[wp.peer.ID] {
					available = append(available, wp)
				}
			}
			if len(available) == 0 {
				break
			}
			selected := r.weightedSelect(available, randomnessFactor)
			if selected == nil {
				continue
			}
			result = append(result, RelayInfo{
				PeerID:   selected.peer.ID,
				AddrInfo: selected.peer,
				Latency:  latencies[selected.peer.ID],
			})
			used[selected.peer.ID] = true
		}
	}
	return result
}

func (r *RelayDiscovery) weightedSelect(peers []weightedPeer, randomnessFactor float64) *weightedPeer {
	if len(peers) == 0 {
		return nil
	}
	if len(peers) == 1 {
		return &peers[0]
	}

	var totalWeight float64
	for _, p := range peers {
		randomWeight := rand.Float64() * randomnessFactor
		adjustedWeight := p.weight*(1-randomnessFactor) + randomWeight
		totalWeight += adjustedWeight
	}

	rVal := rand.Float64() * totalWeight
	var cumulative float64
	for i, p := range peers {
		randomWeight := rand.Float64() * randomnessFactor
		adjustedWeight := p.weight*(1-randomnessFactor) + randomWeight
		cumulative += adjustedWeight
		if rVal <= cumulative {
			return &peers[i]
		}
	}
	return &peers[len(peers)-1]
}

// SelectRelaysForCircuit selects a set of relays for a single circuit.
func (r *RelayDiscovery) SelectRelaysForCircuit(ctx context.Context, peers []peer.AddrInfo, hopCount int, randomnessFactor float64) ([]RelayInfo, error) {
	sampled := r.randomSample(peers, r.samplingSize)
	latencies, err := r.measureLatencies(ctx, sampled)
	if err != nil {
		return nil, err
	}

	var weightedPeers []weightedPeer
	for _, p := range sampled {
		lat := latencies[p.ID]
		if lat == 0 {
			lat = 100 * time.Millisecond
		}
		weight := 1.0 / float64(lat.Milliseconds()+1)
		weightedPeers = append(weightedPeers, weightedPeer{p, weight})
	}

	var result []RelayInfo
	used := make(map[peer.ID]bool)

	for i := 0; i < hopCount && len(result) < hopCount; i++ {
		var available []weightedPeer
		for _, wp := range weightedPeers {
			if !used[wp.peer.ID] {
				available = append(available, wp)
			}
		}
		if len(available) == 0 {
			break
		}
		selected := r.weightedSelect(available, randomnessFactor)
		if selected != nil {
			result = append(result, RelayInfo{
				PeerID:   selected.peer.ID,
				AddrInfo: selected.peer,
				Latency:  latencies[selected.peer.ID],
			})
			used[selected.peer.ID] = true
		}
	}

	if len(result) < hopCount {
		return nil, fmt.Errorf("insufficient relays")
	}
	return result, nil
}

// FilterByExclusion filters out the specified peer IDs from the list of candidates.
func FilterByExclusion(peers []peer.AddrInfo, exclude ...peer.ID) []peer.AddrInfo {
	excludeMap := make(map[peer.ID]bool)
	for _, id := range exclude {
		excludeMap[id] = true
	}
	var result []peer.AddrInfo
	for _, p := range peers {
		if !excludeMap[p.ID] {
			result = append(result, p)
		}
	}
	return result
}

// SortByLatency sorts a slice of RelayInfo by their latency.
func SortByLatency(peers []RelayInfo) {
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].Latency < peers[j].Latency
	})
}
