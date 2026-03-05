package discovery

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// testAddr returns a dummy multiaddr for use in tests.
func testAddr() multiaddr.Multiaddr {
	a, _ := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/4001")
	return a
}

func TestRelayDiscovery_NewRelayDiscovery(t *testing.T) {
	rd := NewRelayDiscovery("/lib-mix/1.0.0", 10, "rtt", 0.3)

	if rd == nil {
		t.Error("expected non-nil discovery")
	}
}

func TestRelayDiscovery_FilterPeers(t *testing.T) {
	rd := NewRelayDiscovery("/lib-mix/1.0.0", 10, "rtt", 0.3)

	peers := []peer.AddrInfo{
		{ID: "peer1"},                                           // No addrs - should be filtered
		{ID: "peer2", Addrs: nil},                               // Should be filtered
		{ID: "peer3", Addrs: []multiaddr.Multiaddr{}},           // Empty - should be filtered
		{ID: "peer4", Addrs: []multiaddr.Multiaddr{testAddr()}}, // Has addr - should pass
	}

	filtered := rd.filterPeers(peers)

	if len(filtered) != 1 {
		t.Errorf("expected 1 peer, got %d", len(filtered))
	}

	if filtered[0].ID != "peer4" {
		t.Errorf("expected peer4, got %s", filtered[0].ID)
	}
}

func TestRelayDiscovery_SelectRandom(t *testing.T) {
	rd := NewRelayDiscovery("/lib-mix/1.0.0", 10, "random", 0.3)

	peers := []peer.AddrInfo{
		{ID: "r1", Addrs: []multiaddr.Multiaddr{testAddr()}},
		{ID: "r2", Addrs: []multiaddr.Multiaddr{testAddr()}},
		{ID: "r3", Addrs: []multiaddr.Multiaddr{testAddr()}},
		{ID: "r4", Addrs: []multiaddr.Multiaddr{testAddr()}},
		{ID: "r5", Addrs: []multiaddr.Multiaddr{testAddr()}},
	}

	result, err := rd.selectRandom(peers, 3)
	if err != nil {
		t.Fatalf("selectRandom() error = %v", err)
	}

	if len(result) != 3 {
		t.Errorf("expected 3 relays, got %d", len(result))
	}

	// Check no duplicates
	seen := make(map[peer.ID]bool)
	for _, r := range result {
		if seen[r.PeerID] {
			t.Errorf("duplicate peer: %s", r.PeerID)
		}
		seen[r.PeerID] = true
	}
}

func TestRelayDiscovery_SelectRandom_Insufficient(t *testing.T) {
	rd := NewRelayDiscovery("/lib-mix/1.0.0", 10, "random", 0.3)

	peers := []peer.AddrInfo{
		{ID: "r1", Addrs: []multiaddr.Multiaddr{testAddr()}},
		{ID: "r2", Addrs: []multiaddr.Multiaddr{testAddr()}},
	}

	_, err := rd.selectRandom(peers, 5)
	if err == nil {
		t.Error("expected error with insufficient peers")
	}
}

func TestRelayDiscovery_RandomSample(t *testing.T) {
	rd := NewRelayDiscovery("/lib-mix/1.0.0", 3, "rtt", 0.3)

	peers := []peer.AddrInfo{
		{ID: "r1"}, {ID: "r2"}, {ID: "r3"},
		{ID: "r4"}, {ID: "r5"},
	}

	sampled := rd.randomSample(peers, 3)

	if len(sampled) != 3 {
		t.Errorf("expected 3 sampled, got %d", len(sampled))
	}

	// All should be from original pool
	original := make(map[peer.ID]bool)
	for _, p := range peers {
		original[p.ID] = true
	}

	for _, p := range sampled {
		if !original[p.ID] {
			t.Errorf("sampled peer not in original pool: %s", p.ID)
		}
	}
}

func TestFilterByExclusion(t *testing.T) {
	peers := []peer.AddrInfo{
		{ID: "r1"}, {ID: "r2"}, {ID: "r3"}, {ID: "r4"},
	}

	filtered := FilterByExclusion(peers, "r2", "r3")

	if len(filtered) != 2 {
		t.Errorf("expected 2 peers, got %d", len(filtered))
	}

	for _, p := range filtered {
		if p.ID == "r2" || p.ID == "r3" {
			t.Errorf("excluded peer should not be in result: %s", p.ID)
		}
	}
}

func TestRelayInfo_Available(t *testing.T) {
	info := RelayInfo{
		PeerID:    "test",
		Available: true,
	}

	if !info.Available {
		t.Error("expected available")
	}
}
