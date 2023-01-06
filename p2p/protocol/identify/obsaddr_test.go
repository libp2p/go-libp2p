package identify_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

type mockConn struct {
	local, remote ma.Multiaddr
	peer          peer.ID
	direction     network.Direction
}

var _ network.Conn = &mockConn{}

func newMockConn(dir network.Direction, peer peer.ID, local, remote ma.Multiaddr) network.Conn {
	return &mockConn{
		direction: dir,
		peer:      peer,
		local:     local,
		remote:    remote,
	}
}

func (m *mockConn) Close() error                  { panic("implement me") }
func (m *mockConn) LocalPeer() peer.ID            { panic("implement me") }
func (m *mockConn) LocalPrivateKey() ic.PrivKey   { panic("implement me") }
func (m *mockConn) RemotePeer() peer.ID           { return m.peer }
func (m *mockConn) RemotePublicKey() ic.PubKey    { panic("implement me") }
func (m *mockConn) LocalMultiaddr() ma.Multiaddr  { return m.local }
func (m *mockConn) RemoteMultiaddr() ma.Multiaddr { return m.remote }
func (m *mockConn) Stat() network.ConnStats {
	return network.ConnStats{Stats: network.Stats{Direction: m.direction}}
}
func (m *mockConn) Scope() network.ConnScope                              { panic("implement me") }
func (m *mockConn) ID() string                                            { panic("implement me") }
func (m *mockConn) NewStream(ctx context.Context) (network.Stream, error) { panic("implement me") }
func (m *mockConn) GetStreams() []network.Stream                          { panic("implement me") }
func (m *mockConn) ConnState() network.ConnectionState                    { panic("implement me") }

func newPeer(t *testing.T) peer.ID {
	t.Helper()
	sk, _, err := ic.GenerateECDSAKeyPair(rand.Reader)
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(sk)
	require.NoError(t, err)
	return id
}

func TestObsAddrSet(t *testing.T) {
	local := ma.StringCast("/ip4/127.0.0.1/tcp/10086")
	h, err := libp2p.New(libp2p.ListenAddrs(local))
	require.NoError(t, err)
	defer h.Close()

	oam, err := identify.NewObservedAddrManager(h)
	require.NoError(t, err)

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
	require.Empty(t, oam.Addrs())

	p1 := newPeer(t)
	oam.Record(newMockConn(network.DirOutbound, p1, local, a4), a1)
	oam.Record(newMockConn(network.DirOutbound, p1, local, a4), a2)
	oam.Record(newMockConn(network.DirOutbound, p1, local, a4), a3)
	// these are all different so we should not yet get them.
	time.Sleep(50 * time.Millisecond)
	require.Empty(t, oam.Addrs())

	// same observer, so should not yet get them.
	oam.Record(newMockConn(network.DirOutbound, p1, local, a4), a1)
	oam.Record(newMockConn(network.DirOutbound, p1, local, a4), a2)
	oam.Record(newMockConn(network.DirOutbound, p1, local, a4), a3)
	time.Sleep(50 * time.Millisecond)
	require.Empty(t, oam.Addrs())

	// different observer, but same observer group.
	p2 := newPeer(t)
	oam.Record(newMockConn(network.DirOutbound, p2, local, a5), a1)
	oam.Record(newMockConn(network.DirOutbound, p2, local, a5), a2)
	oam.Record(newMockConn(network.DirOutbound, p2, local, a5), a3)
	time.Sleep(50 * time.Millisecond)
	require.Empty(t, oam.Addrs())

	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, b1), a1)
	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, b2), a1)
	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, b3), a1)
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, []ma.Multiaddr{a1}, oam.Addrs())

	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, a5), a2)
	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, a5), a1)
	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, a5), a1)
	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, b1), a2)
	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, b1), a1)
	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, b1), a1)
	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, b2), a2)
	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, b2), a1)
	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, b2), a1)
	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, b4), a2)
	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, b5), a2)
	time.Sleep(50 * time.Millisecond)
	require.ElementsMatch(t, []ma.Multiaddr{a1, a2}, oam.Addrs())
}

type wrappedNetwork struct {
	network.Network
	conns map[peer.ID][]network.Conn
}

func (n *wrappedNetwork) ConnsToPeer(p peer.ID) []network.Conn {
	return n.conns[p]
}

type wrappedHost struct {
	conns map[peer.ID][]network.Conn
	host.Host
}

func (h *wrappedHost) AddConn(p peer.ID, c network.Conn) {
	if h.conns == nil {
		h.conns = make(map[peer.ID][]network.Conn)
	}
	h.conns[p] = append(h.conns[p], c)
}

func (h *wrappedHost) Network() network.Network {
	return &wrappedNetwork{
		Network: h.Host.Network(),
		conns:   h.conns,
	}
}

func TestObsAddrSetExpiration(t *testing.T) {
	local := ma.StringCast("/ip4/127.0.0.1/tcp/10086")
	h, err := libp2p.New(libp2p.ListenAddrs(local))
	require.NoError(t, err)
	defer h.Close()
	host := &wrappedHost{Host: h}
	oam, err := identify.NewObservedAddrManager(host)
	require.NoError(t, err)
	defer oam.Close()
	notif := (*identify.ObsAddrNotifiee)(oam)

	require.Empty(t, oam.Addrs())

	a1 := ma.StringCast("/ip4/1.2.3.4/tcp/1231")
	a2 := ma.StringCast("/ip4/1.2.3.4/tcp/1232")

	record := func(c network.Conn, observed ma.Multiaddr) {
		host.AddConn(c.RemotePeer(), c)
		oam.Record(c, observed)
	}

	var conns1 []network.Conn
	for i := 0; i < 5; i++ {
		ip := net.IP(make([]byte, 4))
		rand.Read(ip)
		conn1 := newMockConn(network.DirOutbound, newPeer(t), local, ma.StringCast(fmt.Sprintf("/ip4/%s/tcp/1234", ip.String())))
		conns1 = append(conns1, conn1)
		record(conn1, a1)
		record(newMockConn(network.DirOutbound, newPeer(t), local, ma.StringCast(fmt.Sprintf("/ip4/%s/tcp/1234", ip.String()))), a2)
	}
	time.Sleep(50 * time.Millisecond)
	require.ElementsMatch(t, []ma.Multiaddr{a1, a2}, oam.Addrs())

	oam.SetTTL(200 * time.Millisecond)
	// disconnect all addresses that gave us a1
	for _, c := range conns1 {
		notif.Disconnected(h.Network(), c)
	}
	require.Eventually(t, func() bool { return len(oam.Addrs()) < 2 }, time.Second, 10*time.Millisecond)
	require.Equal(t, []ma.Multiaddr{a2}, oam.Addrs())
	time.Sleep(time.Second)
}

func TestObservedAddrFiltering(t *testing.T) {
	local := ma.StringCast("/ip4/127.0.0.1/tcp/10086")
	h, err := libp2p.New(libp2p.ListenAddrs(local))
	require.NoError(t, err)
	defer h.Close()

	oam, err := identify.NewObservedAddrManager(h)
	require.NoError(t, err)
	require.Empty(t, oam.Addrs())

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
	observers := []ma.Multiaddr{b1, b2, b3, b4, b5, b6, b7, b8, b9, b10}

	var peers []peer.ID
	for i := 0; i < 10; i++ {
		peers = append(peers, newPeer(t))
	}
	for i := 0; i < 4; i++ {
		oam.Record(newMockConn(network.DirOutbound, peers[i], local, observers[i]), it1)
		oam.Record(newMockConn(network.DirOutbound, peers[i], local, observers[i]), it2)
		oam.Record(newMockConn(network.DirOutbound, peers[i], local, observers[i]), it3)
		oam.Record(newMockConn(network.DirOutbound, peers[i], local, observers[i]), it4)
		oam.Record(newMockConn(network.DirOutbound, peers[i], local, observers[i]), it5)
		oam.Record(newMockConn(network.DirOutbound, peers[i], local, observers[i]), it6)
		oam.Record(newMockConn(network.DirOutbound, peers[i], local, observers[i]), it7)
		time.Sleep(10 * time.Millisecond) // give the loop some time to process the entries
	}
	oam.Record(newMockConn(network.DirOutbound, peers[4], local, observers[4]), it1)
	oam.Record(newMockConn(network.DirOutbound, peers[4], local, observers[4]), it7)

	time.Sleep(50 * time.Millisecond)
	require.ElementsMatch(t, oam.Addrs(), []ma.Multiaddr{it1, it7})

	// Bump the number of observations so 1 & 7 have 7 observations.
	oam.Record(newMockConn(network.DirOutbound, peers[5], local, observers[5]), it1)
	oam.Record(newMockConn(network.DirOutbound, peers[6], local, observers[6]), it1)
	oam.Record(newMockConn(network.DirOutbound, peers[5], local, observers[5]), it7)
	oam.Record(newMockConn(network.DirOutbound, peers[6], local, observers[6]), it7)

	// Add an observation from IP 1.2.3.13
	// 2 & 3 now have 5 observations
	oam.Record(newMockConn(network.DirOutbound, peers[7], local, observers[7]), it2)
	oam.Record(newMockConn(network.DirOutbound, peers[7], local, observers[7]), it3)

	time.Sleep(50 * time.Millisecond)
	require.ElementsMatch(t, oam.Addrs(), []ma.Multiaddr{it1, it7})

	// Add an inbound observation from IP 1.2.3.13, it should override the
	// existing observation and it should make these addresses win even
	// though we have fewer observations.
	//
	// 2 & 3 now have 6 observations.
	oam.Record(newMockConn(network.DirInbound, peers[8], local, observers[8]), it2)
	oam.Record(newMockConn(network.DirInbound, peers[8], local, observers[8]), it3)

	time.Sleep(50 * time.Millisecond)
	require.ElementsMatch(t, oam.Addrs(), []ma.Multiaddr{it2, it3})

	// Adding an outbound observation shouldn't "downgrade" it.
	//
	// 2 & 3 now have 7 observations.
	oam.Record(newMockConn(network.DirInbound, peers[9], local, observers[9]), it2)
	oam.Record(newMockConn(network.DirInbound, peers[9], local, observers[9]), it3)
	time.Sleep(50 * time.Millisecond)
	require.ElementsMatch(t, oam.Addrs(), []ma.Multiaddr{it2, it3})
}

func TestEmitNATDeviceTypeSymmetric(t *testing.T) {
	local := ma.StringCast("/ip4/127.0.0.1/tcp/10086")
	h, err := libp2p.New(libp2p.ListenAddrs(local))
	require.NoError(t, err)
	defer h.Close()

	oam, err := identify.NewObservedAddrManager(h)
	require.NoError(t, err)
	require.Empty(t, oam.Addrs())

	emitter, err := h.EventBus().Emitter(new(event.EvtLocalReachabilityChanged), eventbus.Stateful)
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

	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, b1), it1)
	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, b2), it2)
	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, b3), it3)
	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, b4), it4)

	sub, err := h.EventBus().Subscribe(new(event.EvtNATDeviceTypeChanged))
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
	local := ma.StringCast("/ip4/127.0.0.1/tcp/10086")
	h, err := libp2p.New(libp2p.ListenAddrs(local))
	require.NoError(t, err)
	defer h.Close()

	oam, err := identify.NewObservedAddrManager(h)
	require.NoError(t, err)
	require.Empty(t, oam.Addrs())

	emitter, err := h.EventBus().Emitter(new(event.EvtLocalReachabilityChanged), eventbus.Stateful)
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

	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, b1), it1)
	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, b2), it2)
	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, b3), it3)
	oam.Record(newMockConn(network.DirOutbound, newPeer(t), local, b4), it4)

	sub, err := h.EventBus().Subscribe(new(event.EvtNATDeviceTypeChanged))
	require.NoError(t, err)
	select {
	case ev := <-sub.Out():
		evt := ev.(event.EvtNATDeviceTypeChanged)
		require.Equal(t, network.NATDeviceTypeCone, evt.NatDeviceType)
	case <-time.After(5 * time.Second):
		t.Fatal("did not get Cone NAT event")
	}
}
