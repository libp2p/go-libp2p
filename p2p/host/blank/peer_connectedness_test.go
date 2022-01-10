package blank

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	swarmt "github.com/libp2p/go-libp2p-swarm/testing"

	"github.com/stretchr/testify/require"
)

func TestPeerConnectedness(t *testing.T) {
	h1, err := NewHost(swarmt.GenSwarm(t))
	require.NoError(t, err)
	defer h1.Close()
	h2, err := NewHost(swarmt.GenSwarm(t))
	require.NoError(t, err)

	sub1, err := h1.EventBus().Subscribe(&event.EvtPeerConnectednessChanged{})
	require.NoError(t, err)
	defer sub1.Close()
	sub2, err := h2.EventBus().Subscribe(&event.EvtPeerConnectednessChanged{})
	require.NoError(t, err)
	defer sub2.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, h1.Connect(ctx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()}))
	require.Equal(t, (<-sub1.Out()).(event.EvtPeerConnectednessChanged), event.EvtPeerConnectednessChanged{
		Peer:          h2.ID(),
		Connectedness: network.Connected,
	})
	require.Equal(t, (<-sub2.Out()).(event.EvtPeerConnectednessChanged), event.EvtPeerConnectednessChanged{
		Peer:          h1.ID(),
		Connectedness: network.Connected,
	})

	// now close h2. This will disconnect it from h1.
	require.NoError(t, h2.Close())
	require.Equal(t, (<-sub1.Out()).(event.EvtPeerConnectednessChanged), event.EvtPeerConnectednessChanged{
		Peer:          h2.ID(),
		Connectedness: network.NotConnected,
	})
}
