package identify

import (
	"context"
	"testing"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"

	blhost "github.com/libp2p/go-libp2p-blankhost"
	swarmt "github.com/libp2p/go-libp2p-swarm/testing"

	"github.com/stretchr/testify/require"
)

func TestMakeApplyDelta(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1 := blhost.NewBlankHost(swarmt.GenSwarm(t, ctx))
	defer h1.Close()
	ids1 := NewIDService(h1)
	ph := newPeerHandler(h1.ID(), ids1)
	ph.start()
	defer ph.close()

	m1 := ph.nextDelta()
	require.NotNil(t, m1)
	// We haven't changed anything since creating the peer handler
	require.Empty(t, m1.AddedProtocols)

	h1.SetStreamHandler("p1", func(network.Stream) {})
	m2 := ph.nextDelta()
	require.Len(t, m2.AddedProtocols, 1)
	require.Contains(t, m2.AddedProtocols, "p1")
	require.Empty(t, m2.RmProtocols)

	h1.SetStreamHandler("p2", func(network.Stream) {})
	h1.SetStreamHandler("p3", func(stream network.Stream) {})
	m3 := ph.nextDelta()
	require.Len(t, m3.AddedProtocols, 2)
	require.Contains(t, m3.AddedProtocols, "p2")
	require.Contains(t, m3.AddedProtocols, "p3")
	require.Empty(t, m3.RmProtocols)

	h1.RemoveStreamHandler("p3")
	m4 := ph.nextDelta()
	require.Empty(t, m4.AddedProtocols)
	require.Len(t, m4.RmProtocols, 1)
	require.Contains(t, m4.RmProtocols, "p3")

	h1.RemoveStreamHandler("p2")
	h1.RemoveStreamHandler("p1")
	m5 := ph.nextDelta()
	require.Empty(t, m5.AddedProtocols)
	require.Len(t, m5.RmProtocols, 2)
	require.Contains(t, m5.RmProtocols, "p2")
	require.Contains(t, m5.RmProtocols, "p1")
}

func TestHandlerClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1 := blhost.NewBlankHost(swarmt.GenSwarm(t, ctx))
	defer h1.Close()
	ids1 := NewIDService(h1)
	ph := newPeerHandler(h1.ID(), ids1)
	ph.start()

	require.NoError(t, ph.close())
}

func TestPeerSupportsProto(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h1 := blhost.NewBlankHost(swarmt.GenSwarm(t, ctx))
	defer h1.Close()
	ids1 := NewIDService(h1)

	rp := peer.ID("test")
	ph := newPeerHandler(rp, ids1)
	require.NoError(t, h1.Peerstore().AddProtocols(rp, "test"))
	require.True(t, ph.peerSupportsProtos([]string{"test"}))
	require.False(t, ph.peerSupportsProtos([]string{"random"}))

	// remove support for protocol and check
	require.NoError(t, h1.Peerstore().RemoveProtocols(rp, "test"))
	require.False(t, ph.peerSupportsProtos([]string{"test"}))
}
