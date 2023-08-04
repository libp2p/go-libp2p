package basichost

import (
	"context"
	"fmt"
	"testing"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestNoStreamOverTransientConnection(t *testing.T) {
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

	h2.SetStreamHandler("/testprotocol", func(s network.Stream) {
		fmt.Println("testprotocol")

		// End the example
		s.Close()
	})

	_, err = client.Reserve(context.Background(), h2, relay1info)
	require.NoError(t, err)

	relayaddr := ma.StringCast("/p2p/" + relay1info.ID.String() + "/p2p-circuit/p2p/" + h2.ID().String())

	h2Info := peer.AddrInfo{
		ID:    h2.ID(),
		Addrs: []ma.Multiaddr{relayaddr},
	}
	err = h1.Connect(context.Background(), h2Info)
	require.NoError(t, err)

	ctx := network.WithNoDial(context.Background(), "test")
	_, err = h1.NewStream(ctx, h2.ID(), "/testprotocol")

	require.ErrorIs(t, err, network.ErrTransientConn)

	_, err = h1.NewStream(network.WithUseTransient(context.Background(), "test"), h2.ID(), "/testprotocol")
	require.NoError(t, err)
}
