package swarm

import (
	"context"
	"crypto/rand"
	"net"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/test"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	quic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/libp2p/go-libp2p/p2p/transport/websocket"
	webtransport "github.com/libp2p/go-libp2p/p2p/transport/webtransport"

	ma "github.com/multiformats/go-multiaddr"
	madns "github.com/multiformats/go-multiaddr-dns"
	"github.com/stretchr/testify/require"
)

func TestAddrsForDial(t *testing.T) {
	mockResolver := madns.MockResolver{IP: make(map[string][]net.IPAddr)}
	ipaddr, err := net.ResolveIPAddr("ip4", "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	mockResolver.IP["example.com"] = []net.IPAddr{*ipaddr}

	resolver, err := madns.NewResolver(madns.WithDomainResolver("example.com", &mockResolver))
	if err != nil {
		t.Fatal(err)
	}

	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(priv)
	require.NoError(t, err)

	ps, err := pstoremem.NewPeerstore()
	require.NoError(t, err)
	ps.AddPubKey(id, priv.GetPublic())
	ps.AddPrivKey(id, priv)
	t.Cleanup(func() { ps.Close() })

	tpt, err := websocket.New(nil, &network.NullResourceManager{})
	require.NoError(t, err)
	s, err := NewSwarm(id, ps, eventbus.NewBus(), WithMultiaddrResolver(resolver))
	require.NoError(t, err)
	defer s.Close()
	err = s.AddTransport(tpt)
	require.NoError(t, err)

	otherPeer := test.RandPeerIDFatal(t)

	ps.AddAddr(otherPeer, ma.StringCast("/dns4/example.com/tcp/1234/wss"), time.Hour)

	ctx := context.Background()
	mas, err := s.addrsForDial(ctx, otherPeer)
	require.NoError(t, err)

	require.NotZero(t, len(mas))
}

func TestDedupAddrsForDial(t *testing.T) {
	mockResolver := madns.MockResolver{IP: make(map[string][]net.IPAddr)}
	ipaddr, err := net.ResolveIPAddr("ip4", "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	mockResolver.IP["example.com"] = []net.IPAddr{*ipaddr}

	resolver, err := madns.NewResolver(madns.WithDomainResolver("example.com", &mockResolver))
	if err != nil {
		t.Fatal(err)
	}

	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(priv)
	require.NoError(t, err)

	ps, err := pstoremem.NewPeerstore()
	require.NoError(t, err)
	ps.AddPubKey(id, priv.GetPublic())
	ps.AddPrivKey(id, priv)
	t.Cleanup(func() { ps.Close() })

	s, err := NewSwarm(id, ps, eventbus.NewBus(), WithMultiaddrResolver(resolver))
	require.NoError(t, err)
	defer s.Close()

	tpt, err := tcp.NewTCPTransport(nil, &network.NullResourceManager{})
	require.NoError(t, err)
	err = s.AddTransport(tpt)
	require.NoError(t, err)

	otherPeer := test.RandPeerIDFatal(t)

	ps.AddAddr(otherPeer, ma.StringCast("/dns4/example.com/tcp/1234"), time.Hour)
	ps.AddAddr(otherPeer, ma.StringCast("/ip4/1.2.3.4/tcp/1234"), time.Hour)

	ctx := context.Background()
	mas, err := s.addrsForDial(ctx, otherPeer)
	require.NoError(t, err)

	require.Equal(t, 1, len(mas))
}

func newTestSwarmWithResolver(t *testing.T, resolver *madns.Resolver) *Swarm {
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(priv)
	require.NoError(t, err)
	ps, err := pstoremem.NewPeerstore()
	require.NoError(t, err)
	ps.AddPubKey(id, priv.GetPublic())
	ps.AddPrivKey(id, priv)
	t.Cleanup(func() { ps.Close() })
	s, err := NewSwarm(id, ps, eventbus.NewBus(), WithMultiaddrResolver(resolver))
	require.NoError(t, err)
	t.Cleanup(func() {
		s.Close()
	})

	// Add a tcp transport so that we know we can dial a tcp multiaddr and we don't filter it out.
	tpt, err := tcp.NewTCPTransport(nil, &network.NullResourceManager{})
	require.NoError(t, err)
	err = s.AddTransport(tpt)
	require.NoError(t, err)

	return s
}

func TestAddrResolution(t *testing.T) {
	ctx := context.Background()

	p1 := test.RandPeerIDFatal(t)
	p2 := test.RandPeerIDFatal(t)
	addr1 := ma.StringCast("/dnsaddr/example.com")
	addr2 := ma.StringCast("/ip4/192.0.2.1/tcp/123")

	p2paddr2 := ma.StringCast("/ip4/192.0.2.1/tcp/123/p2p/" + p1.Pretty())
	p2paddr3 := ma.StringCast("/ip4/192.0.2.1/tcp/123/p2p/" + p2.Pretty())

	backend := &madns.MockResolver{
		TXT: map[string][]string{"_dnsaddr.example.com": {
			"dnsaddr=" + p2paddr2.String(), "dnsaddr=" + p2paddr3.String(),
		}},
	}
	resolver, err := madns.NewResolver(madns.WithDefaultResolver(backend))
	require.NoError(t, err)

	s := newTestSwarmWithResolver(t, resolver)

	s.peers.AddAddr(p1, addr1, time.Hour)

	tctx, cancel := context.WithTimeout(ctx, time.Millisecond*100)
	defer cancel()
	mas, err := s.addrsForDial(tctx, p1)
	require.NoError(t, err)

	require.Len(t, mas, 1)
	require.Contains(t, mas, addr2)

	addrs := s.peers.Addrs(p1)
	require.Len(t, addrs, 2)
	require.Contains(t, addrs, addr1)
	require.Contains(t, addrs, addr2)
}

func TestAddrResolutionRecursive(t *testing.T) {
	ctx := context.Background()

	p1, err := test.RandPeerID()
	if err != nil {
		t.Error(err)
	}
	p2, err := test.RandPeerID()
	if err != nil {
		t.Error(err)
	}
	addr1 := ma.StringCast("/dnsaddr/example.com")
	addr2 := ma.StringCast("/ip4/192.0.2.1/tcp/123")
	p2paddr1 := ma.StringCast("/dnsaddr/example.com/p2p/" + p1.Pretty())
	p2paddr2 := ma.StringCast("/dnsaddr/example.com/p2p/" + p2.Pretty())
	p2paddr1i := ma.StringCast("/dnsaddr/foo.example.com/p2p/" + p1.Pretty())
	p2paddr2i := ma.StringCast("/dnsaddr/bar.example.com/p2p/" + p2.Pretty())
	p2paddr1f := ma.StringCast("/ip4/192.0.2.1/tcp/123/p2p/" + p1.Pretty())

	backend := &madns.MockResolver{
		TXT: map[string][]string{
			"_dnsaddr.example.com": {
				"dnsaddr=" + p2paddr1i.String(),
				"dnsaddr=" + p2paddr2i.String(),
			},
			"_dnsaddr.foo.example.com": {
				"dnsaddr=" + p2paddr1f.String(),
			},
			"_dnsaddr.bar.example.com": {
				"dnsaddr=" + p2paddr2i.String(),
			},
		},
	}
	resolver, err := madns.NewResolver(madns.WithDefaultResolver(backend))
	if err != nil {
		t.Fatal(err)
	}

	s := newTestSwarmWithResolver(t, resolver)

	pi1, err := peer.AddrInfoFromP2pAddr(p2paddr1)
	require.NoError(t, err)

	tctx, cancel := context.WithTimeout(ctx, time.Millisecond*100)
	defer cancel()
	s.Peerstore().AddAddrs(pi1.ID, pi1.Addrs, peerstore.TempAddrTTL)
	_, err = s.addrsForDial(tctx, p1)
	require.NoError(t, err)

	addrs1 := s.Peerstore().Addrs(pi1.ID)
	require.Len(t, addrs1, 2)
	require.Contains(t, addrs1, addr1)
	require.Contains(t, addrs1, addr2)

	pi2, err := peer.AddrInfoFromP2pAddr(p2paddr2)
	require.NoError(t, err)

	s.Peerstore().AddAddrs(pi2.ID, pi2.Addrs, peerstore.TempAddrTTL)
	_, err = s.addrsForDial(tctx, p2)
	// This never resolves to a good address
	require.Equal(t, ErrNoGoodAddresses, err)

	addrs2 := s.Peerstore().Addrs(pi2.ID)
	require.Len(t, addrs2, 1)
	require.Contains(t, addrs2, addr1)
}

func TestLocalHostWebTransportRemoved(t *testing.T) {
	resolver, err := madns.NewResolver()
	if err != nil {
		t.Fatal(err)
	}

	s := newTestSwarmWithResolver(t, resolver)
	p, err := test.RandPeerID()
	if err != nil {
		t.Error(err)
	}
	reuse, err := quicreuse.NewConnManager([32]byte{})
	require.NoError(t, err)
	defer reuse.Close()

	quicTr, err := quic.NewTransport(s.Peerstore().PrivKey(s.LocalPeer()), reuse, nil, nil, nil)
	require.NoError(t, err)
	require.NoError(t, s.AddTransport(quicTr))

	webtransportTr, err := webtransport.New(s.Peerstore().PrivKey(s.LocalPeer()), nil, reuse, nil, nil)
	require.NoError(t, err)
	s.AddTransport(webtransportTr)

	err = s.AddListenAddr(ma.StringCast("/ip4/127.0.0.1/udp/10000/quic-v1/"))
	require.NoError(t, err)

	res := s.filterKnownUndialables(p, []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/udp/10000/quic-v1/webtransport")})
	if len(res) != 0 {
		t.Errorf("failed to filter localhost webtransport address")
	}
	s.Close()
}
