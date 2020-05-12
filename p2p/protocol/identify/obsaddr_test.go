package identify_test

import (
	"context"
	"sync"
	"testing"
	"time"

	detectrace "github.com/ipfs/go-detect-race"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	p2putil "github.com/libp2p/go-libp2p-netutil"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	identify "github.com/libp2p/go-libp2p/p2p/protocol/identify"

	ma "github.com/multiformats/go-multiaddr"
	mafmt "github.com/multiformats/go-multiaddr-fmt"
	"github.com/stretchr/testify/require"
)

type harness struct {
	t *testing.T

	mocknet mocknet.Mocknet
	host    host.Host

	oas *identify.ObservedAddrManager
}

func (h *harness) add(observer ma.Multiaddr) peer.ID {
	// create a new fake peer.
	sk, err := p2putil.RandTestBogusPrivateKey()
	if err != nil {
		h.t.Fatal(err)
	}
	h2, err := h.mocknet.AddPeer(sk, observer)
	if err != nil {
		h.t.Fatal(err)
	}
	_, err = h.mocknet.LinkPeers(h.host.ID(), h2.ID())
	if err != nil {
		h.t.Fatal(err)
	}
	return h2.ID()
}

func (h *harness) conn(observer peer.ID) network.Conn {
	c, err := h.mocknet.ConnectPeers(h.host.ID(), observer)
	if err != nil {
		h.t.Fatal(err)
	}
	return c
}

func (h *harness) observe(observed ma.Multiaddr, observer peer.ID) {
	c := h.conn(observer)
	h.oas.Record(c, observed)
	time.Sleep(1 * time.Millisecond) // let the worker run
}

func newHarness(ctx context.Context, t *testing.T) harness {
	mn := mocknet.New(ctx)
	sk, err := p2putil.RandTestBogusPrivateKey()
	if err != nil {
		t.Fatal(err)
	}

	h, err := mn.AddPeer(sk, ma.StringCast("/ip4/127.0.0.1/tcp/10086"))
	if err != nil {
		t.Fatal(err)
	}

	return harness{
		oas:     identify.NewObservedAddrManager(ctx, h),
		mocknet: mn,
		host:    h,
	}
}

// TestObsAddrSet
func TestObsAddrSet(t *testing.T) {
	m := func(s string) ma.Multiaddr {
		m, err := ma.NewMultiaddr(s)
		if err != nil {
			t.Error(err)
		}
		return m
	}

	addrsMarch := func(a, b []ma.Multiaddr) bool {
		if len(a) != len(b) {
			return false
		}

		for _, aa := range a {
			found := false
			for _, bb := range b {
				if aa.Equal(bb) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}

	a1 := m("/ip4/1.2.3.4/tcp/1231")
	a2 := m("/ip4/1.2.3.4/tcp/1232")
	a3 := m("/ip4/1.2.3.4/tcp/1233")
	a4 := m("/ip4/1.2.3.4/tcp/1234")
	a5 := m("/ip4/1.2.3.4/tcp/1235")

	b1 := m("/ip4/1.2.3.6/tcp/1236")
	b2 := m("/ip4/1.2.3.7/tcp/1237")
	b3 := m("/ip4/1.2.3.8/tcp/1237")
	b4 := m("/ip4/1.2.3.9/tcp/1237")
	b5 := m("/ip4/1.2.3.10/tcp/1237")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	harness := newHarness(ctx, t)

	if !addrsMarch(harness.oas.Addrs(), nil) {
		t.Error("addrs should be empty")
	}

	pa4 := harness.add(a4)
	pa5 := harness.add(a5)

	pb1 := harness.add(b1)
	pb2 := harness.add(b2)
	pb3 := harness.add(b3)
	pb4 := harness.add(b4)
	pb5 := harness.add(b5)

	harness.observe(a1, pa4)
	harness.observe(a2, pa4)
	harness.observe(a3, pa4)

	// these are all different so we should not yet get them.
	if !addrsMarch(harness.oas.Addrs(), nil) {
		t.Error("addrs should _still_ be empty (once)")
	}

	// same observer, so should not yet get them.
	harness.observe(a1, pa4)
	harness.observe(a2, pa4)
	harness.observe(a3, pa4)
	if !addrsMarch(harness.oas.Addrs(), nil) {
		t.Error("addrs should _still_ be empty (same obs)")
	}

	// different observer, but same observer group.
	harness.observe(a1, pa5)
	harness.observe(a2, pa5)
	harness.observe(a3, pa5)
	if !addrsMarch(harness.oas.Addrs(), nil) {
		t.Error("addrs should _still_ be empty (same obs group)")
	}

	harness.observe(a1, pb1)
	harness.observe(a1, pb2)
	harness.observe(a1, pb3)
	if !addrsMarch(harness.oas.Addrs(), []ma.Multiaddr{a1}) {
		t.Error("addrs should only have a1")
	}

	harness.observe(a2, pa5)
	harness.observe(a1, pa5)
	harness.observe(a1, pa5)
	harness.observe(a2, pb1)
	harness.observe(a1, pb1)
	harness.observe(a1, pb1)
	harness.observe(a2, pb2)
	harness.observe(a1, pb2)
	harness.observe(a1, pb2)
	harness.observe(a2, pb4)
	harness.observe(a2, pb5)
	if !addrsMarch(harness.oas.Addrs(), []ma.Multiaddr{a1, a2}) {
		t.Error("addrs should only have a1, a2")
	}

	// force a refresh.
	harness.oas.SetTTL(time.Millisecond * 200)
	<-time.After(time.Millisecond * 210)
	if !addrsMarch(harness.oas.Addrs(), []ma.Multiaddr{a1, a2}) {
		t.Error("addrs should only have a1, a2")
	}

	// disconnect from all but b5.
	for _, p := range harness.host.Network().Peers() {
		if p == pb5 {
			continue
		}
		harness.host.Network().ClosePeer(p)
	}

	// wait for all other addresses to time out.
	<-time.After(time.Millisecond * 210)

	// Should still have a2
	if !addrsMarch(harness.oas.Addrs(), []ma.Multiaddr{a2}) {
		t.Error("should only have a2, have: ", harness.oas.Addrs())
	}

	harness.host.Network().ClosePeer(pb5)

	// wait for all addresses to timeout
	<-time.After(time.Millisecond * 400)

	// Should still have a2
	if !addrsMarch(harness.oas.Addrs(), nil) {
		t.Error("addrs should have timed out")
	}
}

func TestAddAddrsProfile(t *testing.T) {
	if detectrace.WithRace() {
		t.Skip("test too slow when the race detector is running")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	harness := newHarness(ctx, t)

	addr := ma.StringCast("/ip4/1.2.3.4/tcp/1231")
	p := harness.add(ma.StringCast("/ip4/1.2.3.6/tcp/1236"))

	c := harness.conn(p)
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10000; j++ {
				harness.oas.Record(c, addr)
				time.Sleep(1 * time.Millisecond)
			}
		}()
	}

	wg.Wait()
}

func TestObservedAddrFiltering(t *testing.T) {
	addrsMarch := func(a, b []ma.Multiaddr) bool {
		if len(a) != len(b) {
			return false
		}

		for _, aa := range a {
			found := false
			for _, bb := range b {
				if aa.Equal(bb) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	harness := newHarness(ctx, t)
	if !addrsMarch(harness.oas.Addrs(), nil) {
		t.Error("addrs should be empty")
	}
	// Also listen on UDP so udp addresses are accepted
	require.NoError(t, harness.host.Network().Listen(ma.StringCast("/ip4/127.0.0.1/udp/5555")))

	// IP4/TCP
	it1 := ma.StringCast("/ip4/1.2.3.4/tcp/1231")
	it2 := ma.StringCast("/ip4/1.2.3.4/tcp/1232")
	it3 := ma.StringCast("/ip4/1.2.3.4/tcp/1233")
	it4 := ma.StringCast("/ip4/1.2.3.4/tcp/1234")
	it5 := ma.StringCast("/ip4/1.2.3.4/tcp/1235")

	// IP4/UDP
	iu1 := ma.StringCast("/ip4/1.2.3.4/udp/1231")
	iu2 := ma.StringCast("/ip4/1.2.3.4/udp/1232")
	iu3 := ma.StringCast("/ip4/1.2.3.4/udp/1233")
	iu4 := ma.StringCast("/ip4/1.2.3.4/udp/1234")
	iu5 := ma.StringCast("/ip4/1.2.3.4/udp/1235")

	// observers
	b1 := ma.StringCast("/ip4/1.2.3.6/tcp/1236")
	b2 := ma.StringCast("/ip4/1.2.3.7/tcp/1237")
	b3 := ma.StringCast("/ip4/1.2.3.8/tcp/1237")
	b4 := ma.StringCast("/ip4/1.2.3.9/tcp/1237")
	b5 := ma.StringCast("/ip4/1.2.3.10/tcp/1237")

	// it1 will be inbound with one vote.
	// it3 will have max votes.
	// iu2 will be inbound with one vote.
	// iu4 will have max votes
	peers := []peer.ID{harness.add(b1), harness.add(b2), harness.add(b3), harness.add(b4), harness.add(b5)}
	for i := 0; i < 4; i++ {
		harness.observe(it1, peers[i])
		harness.observe(it2, peers[i])
		harness.observe(it3, peers[i])
		harness.observe(it4, peers[i])
		harness.observe(it5, peers[i])

		harness.observe(iu1, peers[i])
		harness.observe(iu2, peers[i])
		harness.observe(iu3, peers[i])
		harness.observe(iu4, peers[i])
		harness.observe(iu5, peers[i])
	}

	harness.observe(it3, peers[4])
	harness.observe(it4, peers[4])
	harness.observe(iu2, peers[4])
	harness.observe(iu5, peers[4])

	addrs := harness.oas.Addrs()
	require.Len(t, addrs, 4)
	require.Contains(t, addrs, it3)
	require.Contains(t, addrs, it4)
	require.Contains(t, addrs, iu2)
	require.Contains(t, addrs, iu5)
}

func TestObservedAddrGroupKey(t *testing.T) {
	ip4 := mafmt.Base(ma.P_IP4)
	udp := mafmt.Base(ma.P_UDP)
	tcp := mafmt.Base(ma.P_TCP)

	oa1 := &identify.ObservedAddr{Addr: ma.StringCast("/ip4/1.2.3.4/tcp/1231")}
	require.Equal(t, mafmt.And(ip4, tcp).String(), oa1.GroupKey())

	oa2 := &identify.ObservedAddr{Addr: ma.StringCast("/ip4/1.2.3.5/tcp/2222")}
	require.Equal(t, mafmt.And(ip4, tcp).String(), oa2.GroupKey())

	oa3 := &identify.ObservedAddr{Addr: ma.StringCast("/ip4/1.2.3.4/udp/1231")}
	require.Equal(t, mafmt.And(ip4, udp).String(), oa3.GroupKey())
	oa4 := &identify.ObservedAddr{Addr: ma.StringCast("/ip4/1.3.3.4/udp/1531")}
	require.Equal(t, mafmt.And(ip4, udp).String(), oa4.GroupKey())
}
