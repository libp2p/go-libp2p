package discovery

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"sort"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// RelayDiscovery handles finding relays via DHT
type RelayDiscovery struct {
	protocolID    string
	samplingSize  int
	selectionMode string // "rtt", "random", "hybrid"
}

// RelayInfo holds information about a potential relay
type RelayInfo struct {
	PeerID    peer.ID
	AddrInfo  peer.AddrInfo
	Latency   time.Duration
	Available bool
}

// NewRelayDiscovery creates a new relay discovery instance
func NewRelayDiscovery(protocolID string, samplingSize int, selectionMode string) *RelayDiscovery {
	return &RelayDiscovery{
		protocolID:    protocolID,
		samplingSize:  samplingSize,
		selectionMode: selectionMode,
	}
}

// FindRelays discovers potential relay nodes and selects them based on selection mode
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
		if len(p.Addrs) > 0 {
			result = append(result, p)
		}
	}
	return result
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

// measureLatencies measures RTT to all provided peers using real TCP connections
func (r *RelayDiscovery) measureLatencies(ctx context.Context, peers []peer.AddrInfo) (map[peer.ID]time.Duration, error) {
	result := make(map[peer.ID]time.Duration)
	type resultChan struct {
		peerID  peer.ID
		latency time.Duration
		err     error
	}

	rc := make(chan resultChan, len(peers))

	for _, p := range peers {
		go func(addrInfo peer.AddrInfo) {
			latency, err := measureRTTToPeer(ctx, addrInfo)
			rc <- resultChan{addrInfo.ID, latency, err}
		}(p)
	}

	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()

	successCount := 0
	for i := 0; i < len(peers); i++ {
		select {
		case <-ctx.Done():
			return result, fmt.Errorf("context cancelled")
		case <-timeout.C:
			return result, fmt.Errorf("latency measurement timeout after 5s")
		case res := <-rc:
			if res.err == nil {
				result[res.peerID] = res.latency
				successCount++
			}
		}
	}

	if successCount == 0 {
		return nil, fmt.Errorf("no successful latency measurements")
	}
	return result, nil
}

// measureRTTToPeer measures round-trip time to a peer using real TCP connection
func measureRTTToPeer(ctx context.Context, addrInfo peer.AddrInfo) (time.Duration, error) {
	if len(addrInfo.Addrs) == 0 {
		return 0, fmt.Errorf("no addresses")
	}

	// Find TCP address from multiaddr
	var tcpAddr string
	for _, addr := range addrInfo.Addrs {
		addrStr := addr.String()
		if contains(addrStr, "/tcp/") {
			tcpAddr = extractHostPort(addrStr)
			break
		}
	}

	if tcpAddr == "" {
		// Fallback to first address
		tcpAddr = extractHostPort(addrInfo.Addrs[0].String())
	}

	if tcpAddr == "" {
		return 0, fmt.Errorf("could not extract TCP address")
	}

	// Real TCP connection with timeout
	dialer := &net.Dialer{Timeout: 5 * time.Second}

	start := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", tcpAddr)
	if err != nil {
		return 0, fmt.Errorf("connection failed: %w", err)
	}
	defer conn.Close()

	// Measure actual RTT
	return time.Since(start), nil
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || (len(s) > 0 && containsHelper(s, substr)))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func extractHostPort(addr string) string {
	// Extract host:port from multiaddr like /ip4/127.0.0.1/tcp/4001
	// Find the /tcp/ part and extract what comes after
	for i := 0; i < len(addr)-5; i++ {
		if i+4 < len(addr) && addr[i:i+4] == "/tcp" {
			// Find the port number after /tcp/
			j := i + 5
			for j < len(addr) && (addr[j] < '0' || addr[j] > '9') {
				j++
			}
			if j < len(addr) {
				// Extract host (everything between /ip4/ or /ip6/ and /tcp/)
				var host string
				for k := 0; k < i; k++ {
					if k+4 < len(addr) && addr[k:k+4] == "/ip4" {
						// Find the IP
						l := k + 4
						if l < len(addr) && addr[l] == '/' {
							l++
						}
						host = addr[l:i]
						break
					}
				}
				if host != "" {
					return host + addr[i+4:j]
				}
			}
		}
	}
	// Simple fallback
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == '/' {
			return addr[:i]
		}
	}
	return addr
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

func (r *RelayDiscovery) SelectRelaysForCircuit(peers []peer.AddrInfo, hopCount int, randomnessFactor float64) ([]RelayInfo, error) {
	sampled := r.randomSample(peers, r.samplingSize)
	latencies, err := r.measureLatencies(context.Background(), sampled)
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

func SortByLatency(peers []RelayInfo) {
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].Latency < peers[j].Latency
	})
}
