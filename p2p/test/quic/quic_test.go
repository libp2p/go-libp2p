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

func TestQUICVersions(t *testing.T) {
	h1, err := libp2p.New(
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.Transport(webtransport.New),
		libp2p.ListenAddrStrings(
			"/ip4/127.0.0.1/udp/12345/quic",    // QUIC draft-29
			"/ip4/127.0.0.1/udp/12345/quic-v1", // QUIC v1
		),
	)
	require.NoError(t, err)
	defer h1.Close()

	addrs := h1.Addrs()
	require.Len(t, addrs, 2)
	var quicDraft29Addr, quicV1Addr ma.Multiaddr
	for _, addr := range addrs {
		switch getQUICMultiaddrCode(addr) {
		case ma.P_QUIC:
			quicDraft29Addr = addr
		case ma.P_QUIC_V1:
			quicV1Addr = addr
		}
	}
	require.NotNil(t, quicDraft29Addr, "expected to be listening on a QUIC draft-29 address")
	require.NotNil(t, quicV1Addr, "expected to be listening on a QUIC v1 address")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	//  connect using QUIC draft-29
	h2, err := libp2p.New(
		libp2p.Transport(libp2pquic.NewTransport),
	)
	require.NoError(t, err)
	require.NoError(t, h2.Connect(ctx, peer.AddrInfo{ID: h1.ID(), Addrs: []ma.Multiaddr{quicDraft29Addr}}))
	conns := h2.Network().ConnsToPeer(h1.ID())
	require.Len(t, conns, 1)
	require.Equal(t, ma.P_QUIC, getQUICMultiaddrCode(conns[0].LocalMultiaddr()))
	require.Equal(t, ma.P_QUIC, getQUICMultiaddrCode(conns[0].RemoteMultiaddr()))
	h2.Close()

	//  connect using QUIC v1
	h3, err := libp2p.New(
		libp2p.Transport(libp2pquic.NewTransport),
	)
	require.NoError(t, err)
	require.NoError(t, h3.Connect(ctx, peer.AddrInfo{ID: h1.ID(), Addrs: []ma.Multiaddr{quicV1Addr}}))
	conns = h3.Network().ConnsToPeer(h1.ID())
	require.Len(t, conns, 1)
	require.Equal(t, ma.P_QUIC_V1, getQUICMultiaddrCode(conns[0].LocalMultiaddr()))
	require.Equal(t, ma.P_QUIC_V1, getQUICMultiaddrCode(conns[0].RemoteMultiaddr()))
	h3.Close()
}

func TestDisableQUICDraft29(t *testing.T) {
	h1, err := libp2p.New(
		libp2p.QUICReuse(quicreuse.NewConnManager, quicreuse.DisableDraft29()),
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.Transport(webtransport.New),
		libp2p.ListenAddrStrings(
			"/ip4/127.0.0.1/udp/12346/quic",    // QUIC draft-29
			"/ip4/127.0.0.1/udp/12346/quic-v1", // QUIC v1
		),
	)
	require.NoError(t, err)
	defer h1.Close()

	addrs := h1.Addrs()
	require.Len(t, addrs, 1)
	require.Equal(t, ma.P_QUIC_V1, getQUICMultiaddrCode(addrs[0]))

	//  connect using QUIC draft-29
	h2, err := libp2p.New(
		libp2p.Transport(libp2pquic.NewTransport),
	)
	require.NoError(t, err)
	defer h2.Close()
	// We disabled QUIC Version Negotiation, so we will _not_ receive a Version Negotiation packet.
	// Instead, the connection will run into the context timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Microsecond)
	ctx = network.WithDialPeerTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	require.ErrorIs(t,
		h2.Connect(ctx, peer.AddrInfo{ID: h1.ID(), Addrs: []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/udp/12346/quic")}}),
		context.DeadlineExceeded,
	)
	// make sure that dialing QUIC v1 works
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.Eventually(t, func() bool {
		// We might still get an error from a previous dial attempt.
		err = h2.Connect(ctx, peer.AddrInfo{ID: h1.ID(), Addrs: []ma.Multiaddr{addrs[0]}})
		return err == nil
	}, 5*time.Second, 100*time.Millisecond)
}

func TestQUICAndWebTransport(t *testing.T) {
	h1, err := libp2p.New(
		libp2p.QUICReuse(quicreuse.NewConnManager),
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.Transport(webtransport.New),
		libp2p.ListenAddrStrings(
			"/ip4/127.0.0.1/udp/12347/quic/",
			"/ip4/127.0.0.1/udp/12347/quic-v1",
			"/ip4/127.0.0.1/udp/12347/quic-v1/webtransport",
		),
	)
	require.NoError(t, err)
	defer h1.Close()

	addrs := h1.Addrs()
	require.Len(t, addrs, 3)
	var quicDraft29Addr, quicV1Addr, webtransportAddr ma.Multiaddr
	for _, addr := range addrs {
		if _, err := addr.ValueForProtocol(ma.P_WEBTRANSPORT); err == nil {
			webtransportAddr = addr
		} else if _, err := addr.ValueForProtocol(ma.P_QUIC_V1); err == nil {
			quicV1Addr = addr
		} else {
			quicDraft29Addr = addr
		}
	}
	require.NotNil(t, webtransportAddr, "expected to have a WebTransport address")
	require.NotNil(t, quicDraft29Addr, "expected to have a QUIC draft-29 address")
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

	// then test that we can dial a QUIC draft-29
	h3, err := libp2p.New(
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.NoListenAddrs,
	)
	require.NoError(t, err)
	require.NoError(t, h3.Connect(ctx, peer.AddrInfo{
		ID: h1.ID(),
		// a libp2p host will prefer dialing v1 if it supports both versions,
		// so we need to filter the addresses so it thinks that h1 only supports draft-29
		Addrs: ma.FilterAddrs(h1.Addrs(), func(addr ma.Multiaddr) bool { _, err := addr.ValueForProtocol(ma.P_QUIC); return err == nil }),
	}))
	for _, conns := range [][]network.Conn{h3.Network().ConnsToPeer(h1.ID()), h1.Network().ConnsToPeer(h3.ID())} {
		require.Len(t, conns, 1)
		if _, err := conns[0].LocalMultiaddr().ValueForProtocol(ma.P_WEBTRANSPORT); err == nil {
			t.Fatalf("expected a QUIC connection, got a WebTransport connection (%s <-> %s)", conns[0].LocalMultiaddr(), conns[0].RemoteMultiaddr())
		}
		require.Equal(t, ma.P_QUIC, getQUICMultiaddrCode(conns[0].LocalMultiaddr()))
		require.Equal(t, ma.P_QUIC, getQUICMultiaddrCode(conns[0].RemoteMultiaddr()))
	}
	h3.Close()

	// finally, test that we can dial a WebTransport connection
	h4, err := libp2p.New(
		libp2p.Transport(webtransport.New),
		libp2p.NoListenAddrs,
	)
	require.NoError(t, err)
	require.NoError(t, h4.Connect(ctx, peer.AddrInfo{ID: h1.ID(), Addrs: h1.Addrs()}))
	for _, conns := range [][]network.Conn{h4.Network().ConnsToPeer(h1.ID()), h1.Network().ConnsToPeer(h4.ID())} {
		require.Len(t, conns, 1)
		if _, err := conns[0].LocalMultiaddr().ValueForProtocol(ma.P_WEBTRANSPORT); err != nil {
			t.Fatalf("expected a WebTransport connection, got a QUIC connection (%s <-> %s)", conns[0].LocalMultiaddr(), conns[0].RemoteMultiaddr())
		}
		require.Equal(t, ma.P_QUIC_V1, getQUICMultiaddrCode(conns[0].LocalMultiaddr()))
		require.Equal(t, ma.P_QUIC_V1, getQUICMultiaddrCode(conns[0].RemoteMultiaddr()))
	}
	h4.Close()
}
