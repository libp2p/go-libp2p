package swarm_test

import (
	"context"
	"testing"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestDialPeerTransientConnection(t *testing.T) {
	h1, err := libp2p.New(
		libp2p.NoListenAddrs,
		libp2p.EnableRelay(),
	)
	require.NoError(t, err)

	h2, err := libp2p.New(
		libp2p.NoListenAddrs,
		libp2p.EnableRelay(),
	)
	require.NoError(t, err)

	relay1, err := libp2p.New()
	require.NoError(t, err)

	_, err = relay.New(relay1)
	require.NoError(t, err)

	relay1info := peer.AddrInfo{
		ID:    relay1.ID(),
		Addrs: relay1.Addrs(),
	}
	err = h1.Connect(context.Background(), relay1info)
	require.NoError(t, err)

	err = h2.Connect(context.Background(), relay1info)
	require.NoError(t, err)

	_, err = client.Reserve(context.Background(), h2, relay1info)
	require.NoError(t, err)

	relayaddr := ma.StringCast("/p2p/" + relay1info.ID.String() + "/p2p-circuit/p2p/" + h2.ID().String())

	h1.Peerstore().AddAddr(h2.ID(), relayaddr, peerstore.TempAddrTTL)

	// swarm.DialPeer should connect over transient connections
	conn1, err := h1.Network().DialPeer(context.Background(), h2.ID())
	require.NoError(t, err)
	require.NotNil(t, conn1)

	// Test that repeated calls return the same connection.
	conn2, err := h1.Network().DialPeer(context.Background(), h2.ID())
	require.NoError(t, err)
	require.NotNil(t, conn2)

	require.Equal(t, conn1, conn2)

	// swarm.DialPeer should fail if forceDirect is used
	ctx := network.WithForceDirectDial(context.Background(), "test")
	conn, err := h1.Network().DialPeer(ctx, h2.ID())
	require.Error(t, err)
	require.Nil(t, conn)
}
