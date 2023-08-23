package quic_test

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
	webtransport "github.com/libp2p/go-libp2p/p2p/transport/webtransport"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func getQUICMultiaddrCode(addr ma.Multiaddr) int {
	if _, err := addr.ValueForProtocol(ma.P_QUIC); err == nil {
		return ma.P_QUIC
	}
	if _, err := addr.ValueForProtocol(ma.P_QUIC_V1); err == nil {
		return ma.P_QUIC_V1
	}
	return 0
}

func TestQUICAndWebTransport(t *testing.T) {
	h1, err := libp2p.New(
		libp2p.QUICReuse(quicreuse.NewConnManager),
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.Transport(webtransport.New),
		libp2p.ListenAddrStrings(
			"/ip4/127.0.0.1/udp/12347/quic-v1",
			"/ip4/127.0.0.1/udp/12347/quic-v1/webtransport",
		),
	)
	require.NoError(t, err)
	defer h1.Close()

	addrs := h1.Addrs()
	require.Len(t, addrs, 2)
	var quicV1Addr, webtransportAddr ma.Multiaddr
	for _, addr := range addrs {
		if _, err := addr.ValueForProtocol(ma.P_WEBTRANSPORT); err == nil {
			webtransportAddr = addr
		} else if _, err := addr.ValueForProtocol(ma.P_QUIC_V1); err == nil {
			quicV1Addr = addr
		}
	}
	require.NotNil(t, webtransportAddr, "expected to have a WebTransport address")
	require.NotNil(t, quicV1Addr, "expected to have a QUIC v1 address")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// first test that we can dial a QUIC v1
	h2, err := libp2p.New(
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.NoListenAddrs,
	)
	require.NoError(t, err)
	require.NoError(t, h2.Connect(ctx, peer.AddrInfo{ID: h1.ID(), Addrs: h1.Addrs()}))
	for _, conns := range [][]network.Conn{h2.Network().ConnsToPeer(h1.ID()), h1.Network().ConnsToPeer(h2.ID())} {
		require.Len(t, conns, 1)
		if _, err := conns[0].LocalMultiaddr().ValueForProtocol(ma.P_WEBTRANSPORT); err == nil {
			t.Fatalf("expected a QUIC connection, got a WebTransport connection (%s <-> %s)", conns[0].LocalMultiaddr(), conns[0].RemoteMultiaddr())
		}
		require.Equal(t, ma.P_QUIC_V1, getQUICMultiaddrCode(conns[0].LocalMultiaddr()))
		require.Equal(t, ma.P_QUIC_V1, getQUICMultiaddrCode(conns[0].RemoteMultiaddr()))
	}
	h2.Close()

	// finally, test that we can dial a WebTransport connection
	h3, err := libp2p.New(
		libp2p.Transport(webtransport.New),
		libp2p.NoListenAddrs,
	)
	require.NoError(t, err)
	require.NoError(t, h3.Connect(ctx, peer.AddrInfo{ID: h1.ID(), Addrs: h1.Addrs()}))
	for _, conns := range [][]network.Conn{h3.Network().ConnsToPeer(h1.ID()), h1.Network().ConnsToPeer(h3.ID())} {
		require.Len(t, conns, 1)
		if _, err := conns[0].LocalMultiaddr().ValueForProtocol(ma.P_WEBTRANSPORT); err != nil {
			t.Fatalf("expected a WebTransport connection, got a QUIC connection (%s <-> %s)", conns[0].LocalMultiaddr(), conns[0].RemoteMultiaddr())
		}
		require.Equal(t, ma.P_QUIC_V1, getQUICMultiaddrCode(conns[0].LocalMultiaddr()))
		require.Equal(t, ma.P_QUIC_V1, getQUICMultiaddrCode(conns[0].RemoteMultiaddr()))
	}
	h3.Close()
}
