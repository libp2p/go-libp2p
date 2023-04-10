package identify_test

import (
	"crypto/rand"
	"testing"
	"time"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"

	ma "github.com/multiformats/go-multiaddr"
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
	sk, _, err := ic.GenerateECDSAKeyPair(rand.Reader)
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
	if c.Stat().Direction != network.DirOutbound {
		h.t.Fatal("expected conn direction to be outbound")
	}
	return c
}

func (h *harness) connInbound(observer peer.ID) network.Conn {
	c, err := h.mocknet.ConnectPeers(observer, h.host.ID())
	if err != nil {
		h.t.Fatal(err)
	}

	c = mocknet.ConnComplement(c)
	if c.Stat().Direction != network.DirInbound {
		h.t.Fatal("expected conn direction to be inbound")
	}
	return c
}

func (h *harness) observe(observed ma.Multiaddr, observer peer.ID) network.Conn {
	c := h.conn(observer)
	h.oas.Record(c, observed)
	time.Sleep(200 * time.Millisecond) // let the worker run
	return c
}

func (h *harness) observeInbound(observed ma.Multiaddr, observer peer.ID) network.Conn {
	c := h.connInbound(observer)
	h.oas.Record(c, observed)
	time.Sleep(200 * time.Millisecond) // let the worker run
	return c
}

func newHarness(t *testing.T) harness {
	return newHarnessWithMa(t, ma.StringCast("/ip4/127.0.0.1/tcp/10086"))
}

func newHarnessWithMa(t *testing.T, listenAddr ma.Multiaddr) harness {
	mn := mocknet.New()
	sk, _, err := ic.GenerateECDSAKeyPair(rand.Reader)
	require.NoError(t, err)
	h, err := mn.AddPeer(sk, listenAddr)
	require.NoError(t, err)
	oas, err := identify.NewObservedAddrManager(h)
	require.NoError(t, err)
	t.Cleanup(func() {
		mn.Close()
		oas.Close()
	})
	return harness{
		oas:     oas,
		mocknet: mn,
		host:    h,
		t:       t,
	}
}

// TestObsAddrSet
func TestObsAddrSet(t *testing.T) {
	addrsMatch := func(a, b []ma.Multiaddr) bool {
		if len(a) != len(b) {
			return false
		}
		for _, aa := range a {
			if !ma.Contains(b, aa) {
				return false
			}
		}
		return true
	}

	a1 := ma.StringCast("/ip4/1.2.3.4/tcp/1231")
	a2 := ma.StringCast("/ip4/1.2.3.4/tcp/1232")
	a3 := ma.StringCast("/ip4/1.2.3.4/tcp/1233")
	a4 := ma.StringCast("/ip4/1.2.3.4/tcp/1234")
	a5 := ma.StringCast("/ip4/1.2.3.4/tcp/1235")

	b1 := ma.StringCast("/ip4/1.2.3.6/tcp/1236")
	b2 := ma.StringCast("/ip4/1.2.3.7/tcp/1237")
	b3 := ma.StringCast("/ip4/1.2.3.8/tcp/1237")
	b4 := ma.StringCast("/ip4/1.2.3.9/tcp/1237")
	b5 := ma.StringCast("/ip4/1.2.3.10/tcp/1237")

	harness := newHarness(t)
	if !addrsMatch(harness.oas.Addrs(), nil) {
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
	if !addrsMatch(harness.oas.Addrs(), nil) {
		t.Error("addrs should _still_ be empty (once)")
	}

	// same observer, so should not yet get them.
	harness.observe(a1, pa4)
	harness.observe(a2, pa4)
	harness.observe(a3, pa4)
	if !addrsMatch(harness.oas.Addrs(), nil) {
		t.Error("addrs should _still_ be empty (same obs)")
	}

	// different observer, but same observer group.
	harness.observe(a1, pa5)
	harness.observe(a2, pa5)
	harness.observe(a3, pa5)
	if !addrsMatch(harness.oas.Addrs(), nil) {
		t.Error("addrs should _still_ be empty (same obs group)")
	}

	harness.observe(a1, pb1)
	harness.observe(a1, pb2)
	harness.observe(a1, pb3)
	if !addrsMatch(harness.oas.Addrs(), []ma.Multiaddr{a1}) {
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
	if !addrsMatch(harness.oas.Addrs(), []ma.Multiaddr{a1, a2}) {
		t.Error("addrs should only have a1, a2")
	}

	// force a refresh.
	harness.oas.SetTTL(time.Millisecond * 200)
	require.Eventuallyf(t,
		func() bool { return addrsMatch(harness.oas.Addrs(), []ma.Multiaddr{a1, a2}) },
		time.Second,
		50*time.Millisecond,
		"addrs should only have %s, %s; have %s", a1, a2, harness.oas.Addrs(),
	)

	// disconnect from all but b5.
	for _, p := range harness.host.Network().Peers() {
		if p == pb5 {
			continue
		}
		harness.host.Network().ClosePeer(p)
	}

	// Wait for all other addresses to time out.
	// After that, we hould still have a2.
	require.Eventuallyf(t,
		func() bool { return addrsMatch(harness.oas.Addrs(), []ma.Multiaddr{a2}) },
		time.Second,
		50*time.Millisecond,
		"should only have a2 (%s), have: %v", a2, harness.oas.Addrs(),
	)
	harness.host.Network().ClosePeer(pb5)

	// wait for all addresses to timeout
	require.Eventually(t,
		func() bool { return len(harness.oas.Addrs()) == 0 },
		400*time.Millisecond,
		20*time.Millisecond,
		"addrs should have timed out",
	)
}

func TestObservedAddrFiltering(t *testing.T) {
	harness := newHarness(t)
	require.Empty(t, harness.oas.Addrs())

	// IP4/TCP
	it1 := ma.StringCast("/ip4/1.2.3.4/tcp/1231")
	it2 := ma.StringCast("/ip4/1.2.3.4/tcp/1232")
	it3 := ma.StringCast("/ip4/1.2.3.4/tcp/1233")
	it4 := ma.StringCast("/ip4/1.2.3.4/tcp/1234")
	it5 := ma.StringCast("/ip4/1.2.3.4/tcp/1235")
	it6 := ma.StringCast("/ip4/1.2.3.4/tcp/1236")
	it7 := ma.StringCast("/ip4/1.2.3.4/tcp/1237")

	// observers
	b1 := ma.StringCast("/ip4/1.2.3.6/tcp/1236")
	b2 := ma.StringCast("/ip4/1.2.3.7/tcp/1237")
	b3 := ma.StringCast("/ip4/1.2.3.8/tcp/1237")
	b4 := ma.StringCast("/ip4/1.2.3.9/tcp/1237")
	b5 := ma.StringCast("/ip4/1.2.3.10/tcp/1237")

	b6 := ma.StringCast("/ip4/1.2.3.11/tcp/1237")
	b7 := ma.StringCast("/ip4/1.2.3.12/tcp/1237")

	// These are all observers in the same group.
	b8 := ma.StringCast("/ip4/1.2.3.13/tcp/1237")
	b9 := ma.StringCast("/ip4/1.2.3.13/tcp/1238")
	b10 := ma.StringCast("/ip4/1.2.3.13/tcp/1239")

	peers := []peer.ID{
		harness.add(b1),
		harness.add(b2),
		harness.add(b3),
		harness.add(b4),
		harness.add(b5),

		harness.add(b6),
		harness.add(b7),

		harness.add(b8),
		harness.add(b9),
		harness.add(b10),
	}
	for i := 0; i < 4; i++ {
		harness.observe(it1, peers[i])
		harness.observe(it2, peers[i])
		harness.observe(it3, peers[i])
		harness.observe(it4, peers[i])
		harness.observe(it5, peers[i])
		harness.observe(it6, peers[i])
		harness.observe(it7, peers[i])
	}

	harness.observe(it1, peers[4])
	harness.observe(it7, peers[4])

	addrs := harness.oas.Addrs()
	require.Len(t, addrs, 2)
	require.Contains(t, addrs, it1)
	require.Contains(t, addrs, it7)

	// Bump the number of observations so 1 & 7 have 7 observations.
	harness.observe(it1, peers[5])
	harness.observe(it1, peers[6])
	harness.observe(it7, peers[5])
	harness.observe(it7, peers[6])

	// Add an observation from IP 1.2.3.13
	// 2 & 3 now have 5 observations
	harness.observe(it2, peers[7])
	harness.observe(it3, peers[7])

	addrs = harness.oas.Addrs()
	require.Len(t, addrs, 2)
	require.Contains(t, addrs, it1)
	require.Contains(t, addrs, it7)

	// Add an inbound observation from IP 1.2.3.13, it should override the
	// existing observation and it should make these addresses win even
	// though we have fewer observations.
	//
	// 2 & 3 now have 6 observations.
	harness.observeInbound(it2, peers[8])
	harness.observeInbound(it3, peers[8])
	addrs = harness.oas.Addrs()
	require.Len(t, addrs, 2)
	require.Contains(t, addrs, it2)
	require.Contains(t, addrs, it3)

	// Adding an outbound observation shouldn't "downgrade" it.
	//
	// 2 & 3 now have 7 observations.
	harness.observe(it2, peers[9])
	harness.observe(it3, peers[9])
	addrs = harness.oas.Addrs()
	require.Len(t, addrs, 2)
	require.Contains(t, addrs, it2)
	require.Contains(t, addrs, it3)
}

func TestEmitNATDeviceTypeSymmetric(t *testing.T) {
	harness := newHarness(t)
	require.Empty(t, harness.oas.Addrs())
	emitter, err := harness.host.EventBus().Emitter(new(event.EvtLocalReachabilityChanged), eventbus.Stateful)
	require.NoError(t, err)
	require.NoError(t, emitter.Emit(event.EvtLocalReachabilityChanged{Reachability: network.ReachabilityPrivate}))

	// TCP
	it1 := ma.StringCast("/ip4/1.2.3.4/tcp/1231")
	it2 := ma.StringCast("/ip4/1.2.3.4/tcp/1232")
	it3 := ma.StringCast("/ip4/1.2.3.4/tcp/1233")
	it4 := ma.StringCast("/ip4/1.2.3.4/tcp/1234")

	// observers
	b1 := ma.StringCast("/ip4/1.2.3.6/tcp/1236")
	b2 := ma.StringCast("/ip4/1.2.3.7/tcp/1237")
	b3 := ma.StringCast("/ip4/1.2.3.8/tcp/1237")
	b4 := ma.StringCast("/ip4/1.2.3.9/tcp/1237")

	peers := []peer.ID{
		harness.add(b1),
		harness.add(b2),
		harness.add(b3),
		harness.add(b4),
	}

	harness.observe(it1, peers[0])
	harness.observe(it2, peers[1])
	harness.observe(it3, peers[2])
	harness.observe(it4, peers[3])

	sub, err := harness.host.EventBus().Subscribe(new(event.EvtNATDeviceTypeChanged))
	require.NoError(t, err)
	select {
	case ev := <-sub.Out():
		evt := ev.(event.EvtNATDeviceTypeChanged)
		require.Equal(t, network.NATDeviceTypeSymmetric, evt.NatDeviceType)
		require.Equal(t, network.NATTransportTCP, evt.TransportProtocol)
	case <-time.After(5 * time.Second):
		t.Fatal("did not get Symmetric NAT event")
	}

}

func TestEmitNATDeviceTypeCone(t *testing.T) {
	harness := newHarness(t)
	require.Empty(t, harness.oas.Addrs())
	emitter, err := harness.host.EventBus().Emitter(new(event.EvtLocalReachabilityChanged), eventbus.Stateful)
	require.NoError(t, err)
	require.NoError(t, emitter.Emit(event.EvtLocalReachabilityChanged{Reachability: network.ReachabilityPrivate}))

	it1 := ma.StringCast("/ip4/1.2.3.4/tcp/1231")
	it2 := ma.StringCast("/ip4/1.2.3.4/tcp/1231")
	it3 := ma.StringCast("/ip4/1.2.3.4/tcp/1231")
	it4 := ma.StringCast("/ip4/1.2.3.4/tcp/1231")

	// observers
	b1 := ma.StringCast("/ip4/1.2.3.6/tcp/1236")
	b2 := ma.StringCast("/ip4/1.2.3.7/tcp/1237")
	b3 := ma.StringCast("/ip4/1.2.3.8/tcp/1237")
	b4 := ma.StringCast("/ip4/1.2.3.9/tcp/1237")

	peers := []peer.ID{
		harness.add(b1),
		harness.add(b2),
		harness.add(b3),
		harness.add(b4),
	}

	harness.observe(it1, peers[0])
	harness.observe(it2, peers[1])
	harness.observe(it3, peers[2])
	harness.observe(it4, peers[3])

	sub, err := harness.host.EventBus().Subscribe(new(event.EvtNATDeviceTypeChanged))
	require.NoError(t, err)
	select {
	case ev := <-sub.Out():
		evt := ev.(event.EvtNATDeviceTypeChanged)
		require.Equal(t, network.NATDeviceTypeCone, evt.NatDeviceType)
	case <-time.After(5 * time.Second):
		t.Fatal("did not get Cone NAT event")
	}
}

func TestObserveWebtransport(t *testing.T) {
	listenAddr := ma.StringCast("/ip4/1.2.3.4/udp/9999/quic-v1/webtransport/certhash/uEgNmb28")
	observedAddr := ma.StringCast("/ip4/1.2.3.4/udp/1231/quic-v1/webtransport")

	harness := newHarnessWithMa(t, listenAddr)

	pb1 := harness.add(ma.StringCast("/ip4/1.2.3.6/udp/1236/quic-v1/webtransport"))
	pb2 := harness.add(ma.StringCast("/ip4/1.2.3.7/udp/1237/quic-v1/webtransport"))
	pb3 := harness.add(ma.StringCast("/ip4/1.2.3.8/udp/1237/quic-v1/webtransport"))
	pb4 := harness.add(ma.StringCast("/ip4/1.2.3.9/udp/1237/quic-v1/webtransport"))
	pb5 := harness.add(ma.StringCast("/ip4/1.2.3.10/udp/1237/quic-v1/webtransport"))

	harness.observe(observedAddr, pb1)
	harness.observe(observedAddr, pb2)
	harness.observe(observedAddr, pb3)
	harness.observe(observedAddr, pb4)
	harness.observe(observedAddr, pb5)

	require.Equal(t, 1, len(harness.oas.Addrs()))
	require.Equal(t, "/ip4/1.2.3.4/udp/1231/quic-v1/webtransport", harness.oas.Addrs()[0].String())
}
