package swarm_test

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	. "github.com/libp2p/go-libp2p/p2p/net/swarm"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func newSwarmWithSubscription(t *testing.T) (*Swarm, event.Subscription) {
	t.Helper()
	bus := eventbus.NewBus()
	sw := swarmt.GenSwarm(t, swarmt.EventBus(bus))
	t.Cleanup(func() { sw.Close() })
	sub, err := bus.Subscribe(new(event.EvtPeerConnectednessChanged))
	require.NoError(t, err)
	t.Cleanup(func() { sub.Close() })
	return sw, sub
}

func checkEvent(t *testing.T, sub event.Subscription, expected event.EvtPeerConnectednessChanged) {
	t.Helper()
	select {
	case ev, ok := <-sub.Out():
		require.True(t, ok)
		evt := ev.(event.EvtPeerConnectednessChanged)
		require.Equal(t, expected.Connectedness, evt.Connectedness, "wrong connectedness state")
		require.Equal(t, expected.Peer, evt.Peer)
	case <-time.After(time.Second):
		t.Fatal("didn't get PeerConnectedness event")
	}

	// check that there are no more events
	select {
	case <-sub.Out():
		t.Fatal("didn't expect any more events")
	case <-time.After(100 * time.Millisecond):
		return
	}
}

func TestConnectednessEventsSingleConn(t *testing.T) {
	s1, sub1 := newSwarmWithSubscription(t)
	s2, sub2 := newSwarmWithSubscription(t)

	s1.Peerstore().AddAddrs(s2.LocalPeer(), []ma.Multiaddr{s2.ListenAddresses()[0]}, time.Hour)
	_, err := s1.DialPeer(context.Background(), s2.LocalPeer())
	require.NoError(t, err)

	checkEvent(t, sub1, event.EvtPeerConnectednessChanged{Peer: s2.LocalPeer(), Connectedness: network.Connected})
	checkEvent(t, sub2, event.EvtPeerConnectednessChanged{Peer: s1.LocalPeer(), Connectedness: network.Connected})

	for _, c := range s2.ConnsToPeer(s1.LocalPeer()) {
		require.NoError(t, c.Close())
	}
	checkEvent(t, sub1, event.EvtPeerConnectednessChanged{Peer: s2.LocalPeer(), Connectedness: network.NotConnected})
	checkEvent(t, sub2, event.EvtPeerConnectednessChanged{Peer: s1.LocalPeer(), Connectedness: network.NotConnected})
}

func TestNoDeadlockWhenConsumingConnectednessEvents(t *testing.T) {
	dialerEventBus := eventbus.NewBus()
	dialer := swarmt.GenSwarm(t, swarmt.OptDialOnly, swarmt.EventBus(dialerEventBus))
	defer dialer.Close()

	listener := swarmt.GenSwarm(t, swarmt.OptDialOnly)
	addrsToListen := []ma.Multiaddr{
		ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1"),
	}

	if err := listener.Listen(addrsToListen...); err != nil {
		t.Fatal(err)
	}
	listenedAddrs := listener.ListenAddresses()

	dialer.Peerstore().AddAddrs(listener.LocalPeer(), listenedAddrs, time.Hour)

	sub, err := dialerEventBus.Subscribe(new(event.EvtPeerConnectednessChanged))
	require.NoError(t, err)

	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// A slow consumer
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-sub.Out():
				time.Sleep(100 * time.Millisecond)
				// Do something with the swarm that needs the conns lock
				_ = dialer.ConnsToPeer(listener.LocalPeer())
				time.Sleep(100 * time.Millisecond)
			}
		}
	}()

	for i := 0; i < 10; i++ {
		// Connect and disconnect to trigger a bunch of events
		_, err := dialer.DialPeer(context.Background(), listener.LocalPeer())
		require.NoError(t, err)
		dialer.ClosePeer(listener.LocalPeer())
	}

	// The test should finish without deadlocking
}
