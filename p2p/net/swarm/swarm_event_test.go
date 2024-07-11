package swarm_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	. "github.com/libp2p/go-libp2p/p2p/net/swarm"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/assert"
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
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

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

func TestConnectednessEvents(t *testing.T) {
	s1, sub1 := newSwarmWithSubscription(t)
	const N = 100
	peers := make([]*Swarm, N)
	for i := 0; i < N; i++ {
		peers[i] = swarmt.GenSwarm(t)
	}

	// First check all connected events
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < N; i++ {
			e := <-sub1.Out()
			evt, ok := e.(event.EvtPeerConnectednessChanged)
			if !ok {
				t.Error("invalid event received", e)
				return
			}
			if evt.Connectedness != network.Connected {
				t.Errorf("invalid event received: expected: Connected, got: %s", evt)
				return
			}
		}
	}()
	for i := 0; i < N; i++ {
		s1.Peerstore().AddAddrs(peers[i].LocalPeer(), []ma.Multiaddr{peers[i].ListenAddresses()[0]}, time.Hour)
		_, err := s1.DialPeer(context.Background(), peers[i].LocalPeer())
		require.NoError(t, err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("expected all connectedness events to be completed")
	}

	// Disconnect some peers
	done = make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < N/2; i++ {
			e := <-sub1.Out()
			evt, ok := e.(event.EvtPeerConnectednessChanged)
			if !ok {
				t.Error("invalid event received", e)
				return
			}
			if evt.Connectedness != network.NotConnected {
				t.Errorf("invalid event received: expected: NotConnected, got: %s", evt)
				return
			}
		}
	}()
	for i := 0; i < N/2; i++ {
		err := s1.ClosePeer(peers[i].LocalPeer())
		require.NoError(t, err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("expected all disconnected events to be completed")
	}

	// Check for disconnected events on swarm close
	done = make(chan struct{})
	go func() {
		defer close(done)
		for i := N / 2; i < N; i++ {
			e := <-sub1.Out()
			evt, ok := e.(event.EvtPeerConnectednessChanged)
			if !ok {
				t.Error("invalid event received", e)
				return
			}
			if evt.Connectedness != network.NotConnected {
				t.Errorf("invalid event received: expected: NotConnected, got: %s", evt)
				return
			}
		}
	}()
	s1.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("expected all disconnected events after swarm close to be completed")
	}
}

func TestConnectednessEventDeadlock(t *testing.T) {
	s1, sub1 := newSwarmWithSubscription(t)
	const N = 100
	peers := make([]*Swarm, N)
	for i := 0; i < N; i++ {
		peers[i] = swarmt.GenSwarm(t)
	}

	// First check all connected events
	done := make(chan struct{})
	go func() {
		defer close(done)
		count := 0
		for count < N {
			e := <-sub1.Out()
			// sleep to simulate a slow consumer
			evt, ok := e.(event.EvtPeerConnectednessChanged)
			if !ok {
				t.Error("invalid event received", e)
				return
			}
			if evt.Connectedness != network.Connected {
				continue
			}
			count++
			s1.ClosePeer(evt.Peer)
		}
	}()
	for i := 0; i < N; i++ {
		s1.Peerstore().AddAddrs(peers[i].LocalPeer(), []ma.Multiaddr{peers[i].ListenAddresses()[0]}, time.Hour)
		go func(i int) {
			_, err := s1.DialPeer(context.Background(), peers[i].LocalPeer())
			assert.NoError(t, err)
		}(i)
	}
	select {
	case <-done:
	case <-time.After(100 * time.Second):
		t.Fatal("expected all connectedness events to be completed")
	}
}

func TestConnectednessEventDeadlockWithDial(t *testing.T) {
	s1, sub1 := newSwarmWithSubscription(t)
	const N = 200
	peers := make([]*Swarm, N)
	for i := 0; i < N; i++ {
		peers[i] = swarmt.GenSwarm(t)
	}
	peers2 := make([]*Swarm, N)
	for i := 0; i < N; i++ {
		peers2[i] = swarmt.GenSwarm(t)
	}

	// First check all connected events
	done := make(chan struct{})
	var subWG sync.WaitGroup
	subWG.Add(1)
	go func() {
		defer subWG.Done()
		count := 0
		for {
			var e interface{}
			select {
			case e = <-sub1.Out():
			case <-done:
				return
			}
			// sleep to simulate a slow consumer
			evt, ok := e.(event.EvtPeerConnectednessChanged)
			if !ok {
				t.Error("invalid event received", e)
				return
			}
			if evt.Connectedness != network.Connected {
				continue
			}
			if count < N {
				time.Sleep(10 * time.Millisecond)
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				s1.Peerstore().AddAddrs(peers2[count].LocalPeer(), []ma.Multiaddr{peers2[count].ListenAddresses()[0]}, time.Hour)
				s1.DialPeer(ctx, peers2[count].LocalPeer())
				count++
				cancel()
			}
		}
	}()
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		s1.Peerstore().AddAddrs(peers[i].LocalPeer(), []ma.Multiaddr{peers[i].ListenAddresses()[0]}, time.Hour)
		go func(i int) {
			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			s1.DialPeer(ctx, peers[i].LocalPeer())
			cancel()
			wg.Done()
		}(i)
	}
	wg.Wait()
	s1.Close()

	close(done)
	subWG.Wait()
}
