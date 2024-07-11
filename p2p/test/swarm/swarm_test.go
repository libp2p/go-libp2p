package swarm_test

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/assert"
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

func TestNewStreamTransientConnection(t *testing.T) {
	h1, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"),
		libp2p.EnableRelay(),
	)
	require.NoError(t, err)

	h2, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"),
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

	// WithAllowLimitedConn should succeed
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	ctx = network.WithAllowLimitedConn(ctx, "test")
	s, err := h1.Network().NewStream(ctx, h2.ID())
	require.NoError(t, err)
	require.NotNil(t, s)
	defer s.Close()

	// Without WithAllowLimitedConn should fail with context deadline exceeded
	ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	s, err = h1.Network().NewStream(ctx, h2.ID())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, s)

	// Provide h2's direct address to h1.
	h1.Peerstore().AddAddrs(h2.ID(), h2.Addrs(), peerstore.TempAddrTTL)
	// network.NoDial should also fail
	ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	ctx = network.WithNoDial(ctx, "test")
	s, err = h1.Network().NewStream(ctx, h2.ID())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, s)

	done := make(chan bool, 2)
	// NewStream should return a stream if an incoming direct connection is established
	go func() {
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ctx = network.WithNoDial(ctx, "test")
		s, err = h1.Network().NewStream(ctx, h2.ID())
		assert.NoError(t, err)
		assert.NotNil(t, s)
		defer s.Close()
		require.Equal(t, network.DirInbound, s.Conn().Stat().Direction)
		done <- true
	}()
	go func() {
		// connect h2 to h1 simulating connection reversal
		h2.Peerstore().AddAddrs(h1.ID(), h1.Addrs(), peerstore.TempAddrTTL)
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		ctx = network.WithForceDirectDial(ctx, "test")
		err := h2.Connect(ctx, peer.AddrInfo{ID: h1.ID()})
		assert.NoError(t, err)
		done <- true
	}()

	<-done
	<-done
}

func TestLimitStreamsWhenHangingHandlers(t *testing.T) {
	var partial rcmgr.PartialLimitConfig
	const streamLimit = 10
	partial.System.Streams = streamLimit
	mgr, err := rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(partial.Build(rcmgr.InfiniteLimits)))
	require.NoError(t, err)

	maddr, err := ma.NewMultiaddr("/ip4/127.0.0.1/udp/0/quic-v1")
	require.NoError(t, err)

	receiver, err := libp2p.New(
		libp2p.ResourceManager(mgr),
		libp2p.ListenAddrs(maddr),
	)
	require.NoError(t, err)
	t.Cleanup(func() { receiver.Close() })

	var wg sync.WaitGroup
	wg.Add(1)

	const pid = "/test"
	receiver.SetStreamHandler(pid, func(s network.Stream) {
		defer s.Close()
		s.Write([]byte{42})
		wg.Wait()
	})

	// Open streamLimit streams
	success := 0
	// we make a lot of tries because identify and identify push take up a few streams
	for i := 0; i < 1000 && success < streamLimit; i++ {
		mgr, err = rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(rcmgr.InfiniteLimits))
		require.NoError(t, err)

		sender, err := libp2p.New(libp2p.ResourceManager(mgr))
		require.NoError(t, err)
		t.Cleanup(func() { sender.Close() })

		sender.Peerstore().AddAddrs(receiver.ID(), receiver.Addrs(), peerstore.PermanentAddrTTL)

		s, err := sender.NewStream(context.Background(), receiver.ID(), pid)
		if err != nil {
			continue
		}

		var b [1]byte
		_, err = io.ReadFull(s, b[:])
		if err == nil {
			success++
		}
		sender.Close()
	}
	require.Equal(t, streamLimit, success)
	// We have the maximum number of streams open. Next call should fail.
	mgr, err = rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(rcmgr.InfiniteLimits))
	require.NoError(t, err)

	sender, err := libp2p.New(libp2p.ResourceManager(mgr))
	require.NoError(t, err)
	t.Cleanup(func() { sender.Close() })

	sender.Peerstore().AddAddrs(receiver.ID(), receiver.Addrs(), peerstore.PermanentAddrTTL)

	_, err = sender.NewStream(context.Background(), receiver.ID(), pid)
	require.Error(t, err)

	// Close the open streams
	wg.Done()

	// Next call should succeed
	require.Eventually(t, func() bool {
		s, err := sender.NewStream(context.Background(), receiver.ID(), pid)
		if err == nil {
			s.Close()
			return true
		}
		return false
	}, 5*time.Second, 100*time.Millisecond)
}
