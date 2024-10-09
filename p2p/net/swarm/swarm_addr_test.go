package swarm_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/test"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"
	circuitv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	webtransport "github.com/libp2p/go-libp2p/p2p/transport/webtransport"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multibase"
	"github.com/multiformats/go-multihash"
	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

func TestDialBadAddrs(t *testing.T) {
	m := func(s string) ma.Multiaddr {
		maddr, err := ma.NewMultiaddr(s)
		if err != nil {
			t.Fatal(err)
		}
		return maddr
	}

	s := makeSwarms(t, 1)[0]

	test := func(a ma.Multiaddr) {
		p := test.RandPeerIDFatal(t)
		s.Peerstore().AddAddr(p, a, peerstore.PermanentAddrTTL)
		if _, err := s.DialPeer(context.Background(), p); err == nil {
			t.Errorf("swarm should not dial: %s", p)
		}
	}

	test(m("/ip6/fe80::1"))                // link local
	test(m("/ip6/fe80::100"))              // link local
	test(m("/ip4/127.0.0.1/udp/1234/utp")) // utp
}

func TestAddrRace(t *testing.T) {
	s := makeSwarms(t, 1)[0]
	defer s.Close()

	a1, err := s.InterfaceListenAddresses()
	require.NoError(t, err)
	a2, err := s.InterfaceListenAddresses()
	require.NoError(t, err)

	if len(a1) > 0 && len(a2) > 0 && &a1[0] == &a2[0] {
		t.Fatal("got the exact same address set twice; this could lead to data races")
	}
}

func TestAddressesWithoutListening(t *testing.T) {
	s := swarmt.GenSwarm(t, swarmt.OptDialOnly)
	a1, err := s.InterfaceListenAddresses()
	require.NoError(t, err)
	require.Empty(t, a1, "expected to be listening on no addresses")
}

func TestDialAddressSelection(t *testing.T) {
	priv, _, err := test.RandTestKeyPair(ic.Ed25519, 256)
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(priv)
	require.NoError(t, err)
	s, err := swarm.NewSwarm("local", nil, eventbus.NewBus())
	require.NoError(t, err)

	tcpTr, err := tcp.NewTCPTransport(nil, nil, nil)
	require.NoError(t, err)
	require.NoError(t, s.AddTransport(tcpTr))
	reuse, err := quicreuse.NewConnManager(quic.StatelessResetKey{}, quic.TokenGeneratorKey{})
	require.NoError(t, err)
	defer reuse.Close()
	quicTr, err := libp2pquic.NewTransport(priv, reuse, nil, nil, nil)
	require.NoError(t, err)
	require.NoError(t, s.AddTransport(quicTr))
	webtransportTr, err := webtransport.New(priv, nil, reuse, nil, nil)
	require.NoError(t, err)
	require.NoError(t, s.AddTransport(webtransportTr))
	h := sha256.Sum256([]byte("foo"))
	hash, err := multihash.Encode(h[:], multihash.SHA2_256)
	require.NoError(t, err)
	certHash, err := multibase.Encode(multibase.Base58BTC, hash)
	require.NoError(t, err)
	circuitTr, err := circuitv2.New(nil, nil)
	require.NoError(t, err)
	require.NoError(t, s.AddTransport(circuitTr))

	require.Equal(t, tcpTr, s.TransportForDialing(ma.StringCast("/ip4/127.0.0.1/tcp/1234")))
	require.Equal(t, quicTr, s.TransportForDialing(ma.StringCast("/ip4/127.0.0.1/udp/1234/quic-v1")))
	require.Equal(t, circuitTr, s.TransportForDialing(ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/udp/1234/quic/p2p-circuit/p2p/%s", id))))
	require.Equal(t, webtransportTr, s.TransportForDialing(ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/udp/1234/quic-v1/webtransport/certhash/%s", certHash))))
	require.Nil(t, s.TransportForDialing(ma.StringCast("/ip4/1.2.3.4")))
	require.Nil(t, s.TransportForDialing(ma.StringCast("/ip4/1.2.3.4/tcp/443/ws")))
}
