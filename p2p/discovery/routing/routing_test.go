package routing

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/libp2p/go-libp2p/p2p/discovery/mocks"
	"github.com/libp2p/go-libp2p/p2p/discovery/util"
	bhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/discovery"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

type mockRoutingTable struct {
	mx        sync.Mutex
	providers map[string]map[peer.ID]peer.AddrInfo
}

type mockRouting struct {
	h   host.Host
	tab *mockRoutingTable
}

func NewMockRoutingTable() *mockRoutingTable {
	return &mockRoutingTable{providers: make(map[string]map[peer.ID]peer.AddrInfo)}
}

func NewMockRouting(h host.Host, tab *mockRoutingTable) *mockRouting {
	return &mockRouting{h: h, tab: tab}
}

func (m *mockRouting) Provide(ctx context.Context, cid cid.Cid, bcast bool) error {
	m.tab.mx.Lock()
	defer m.tab.mx.Unlock()

	pmap, ok := m.tab.providers[cid.String()]
	if !ok {
		pmap = make(map[peer.ID]peer.AddrInfo)
		m.tab.providers[cid.String()] = pmap
	}

	pmap[m.h.ID()] = peer.AddrInfo{ID: m.h.ID(), Addrs: m.h.Addrs()}

	return nil
}

func (m *mockRouting) FindProvidersAsync(ctx context.Context, cid cid.Cid, limit int) <-chan peer.AddrInfo {
	ch := make(chan peer.AddrInfo)
	go func() {
		defer close(ch)
		m.tab.mx.Lock()
		defer m.tab.mx.Unlock()

		pmap, ok := m.tab.providers[cid.String()]
		if !ok {
			return
		}

		for _, pi := range pmap {
			select {
			case ch <- pi:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch
}

func TestRoutingDiscovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1 := bhost.NewBlankHost(swarmt.GenSwarm(t))
	h2 := bhost.NewBlankHost(swarmt.GenSwarm(t))

	mtab := NewMockRoutingTable()
	mr1 := NewMockRouting(h1, mtab)
	mr2 := NewMockRouting(h2, mtab)

	d1 := NewRoutingDiscovery(mr1)
	d2 := NewRoutingDiscovery(mr2)

	_, err := d1.Advertise(ctx, "/test")
	if err != nil {
		t.Fatal(err)
	}

	pis, err := util.FindPeers(ctx, d2, "/test", discovery.Limit(20))
	if err != nil {
		t.Fatal(err)
	}

	if len(pis) != 1 {
		t.Fatalf("Expected 1 peer, got %d", len(pis))
	}

	pi := pis[0]
	if pi.ID != h1.ID() {
		t.Fatalf("Unexpected peer: %s", pi.ID)
	}
}

func TestDiscoveryRouting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1 := bhost.NewBlankHost(swarmt.GenSwarm(t))
	h2 := bhost.NewBlankHost(swarmt.GenSwarm(t))

	clock := clock.NewMock()
	dserver := mocks.NewDiscoveryServer(clock)
	d1 := mocks.NewDiscoveryClient(h1, dserver)
	d2 := mocks.NewDiscoveryClient(h2, dserver)

	r1 := NewDiscoveryRouting(d1, discovery.TTL(time.Hour))
	r2 := NewDiscoveryRouting(d2, discovery.TTL(time.Hour))

	c, err := nsToCid("/test")
	if err != nil {
		t.Fatal(err)
	}

	if err := r1.Provide(ctx, c, true); err != nil {
		t.Fatal(err)
	}

	pch := r2.FindProvidersAsync(ctx, c, 20)

	allAIs := make([]peer.AddrInfo, 0, len(pch))
	for ai := range pch {
		allAIs = append(allAIs, ai)
	}

	if len(allAIs) != 1 {
		t.Fatalf("Expected 1 peer, got %d", len(allAIs))
	}

	ai := allAIs[0]
	if ai.ID != h1.ID() {
		t.Fatalf("Unexpected peer: %s", ai.ID)
	}
}
