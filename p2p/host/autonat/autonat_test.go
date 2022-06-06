package autonat

import (
	"context"
	"testing"
	"time"

	pb "github.com/libp2p/go-libp2p/p2p/host/autonat/pb"
	bhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"

	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"

	"github.com/libp2p/go-msgio/protoio"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

// these are mock service implementations for testing
func makeAutoNATServicePrivate(t *testing.T) host.Host {
	h := bhost.NewBlankHost(swarmt.GenSwarm(t))
	h.SetStreamHandler(AutoNATProto, sayPrivateStreamHandler(t))
	return h
}

func sayPrivateStreamHandler(t *testing.T) network.StreamHandler {
	return func(s network.Stream) {
		defer s.Close()
		r := protoio.NewDelimitedReader(s, network.MessageSizeMax)
		if err := r.ReadMsg(&pb.Message{}); err != nil {
			t.Error(err)
			return
		}
		w := protoio.NewDelimitedWriter(s)
		res := pb.Message{
			Type:         pb.Message_DIAL_RESPONSE.Enum(),
			DialResponse: newDialResponseError(pb.Message_E_DIAL_ERROR, "dial failed"),
		}
		w.WriteMsg(&res)
	}
}

func makeAutoNATServicePublic(t *testing.T) host.Host {
	h := bhost.NewBlankHost(swarmt.GenSwarm(t))
	h.SetStreamHandler(AutoNATProto, func(s network.Stream) {
		defer s.Close()
		r := protoio.NewDelimitedReader(s, network.MessageSizeMax)
		if err := r.ReadMsg(&pb.Message{}); err != nil {
			t.Error(err)
			return
		}
		w := protoio.NewDelimitedWriter(s)
		res := pb.Message{
			Type:         pb.Message_DIAL_RESPONSE.Enum(),
			DialResponse: newDialResponseOK(s.Conn().RemoteMultiaddr()),
		}
		w.WriteMsg(&res)
	})
	return h
}

func makeAutoNAT(t *testing.T, ash host.Host) (host.Host, AutoNAT) {
	h := bhost.NewBlankHost(swarmt.GenSwarm(t))
	h.Peerstore().AddAddrs(ash.ID(), ash.Addrs(), time.Minute)
	h.Peerstore().AddProtocols(ash.ID(), AutoNATProto)
	a, _ := New(h, WithSchedule(100*time.Millisecond, time.Second), WithoutStartupDelay())
	a.(*AmbientAutoNAT).config.dialPolicy.allowSelfDials = true
	a.(*AmbientAutoNAT).config.throttlePeerPeriod = 100 * time.Millisecond
	return h, a
}

func identifyAsServer(server, recip host.Host) {
	recip.Peerstore().AddAddrs(server.ID(), server.Addrs(), time.Minute)
	recip.Peerstore().AddProtocols(server.ID(), AutoNATProto)

}

func connect(t *testing.T, a, b host.Host) {
	pinfo := peer.AddrInfo{ID: a.ID(), Addrs: a.Addrs()}
	err := b.Connect(context.Background(), pinfo)
	if err != nil {
		t.Fatal(err)
	}
}

func expectEvent(t *testing.T, s event.Subscription, expected network.Reachability, timeout time.Duration) {
	t.Helper()
	select {
	case e := <-s.Out():
		ev, ok := e.(event.EvtLocalReachabilityChanged)
		if !ok || ev.Reachability != expected {
			t.Fatal("got wrong event type from the bus")
		}

	case <-time.After(timeout):
		t.Fatal("failed to get the reachability event from the bus")
	}
}

// tests
func TestAutoNATPrivate(t *testing.T) {
	hs := makeAutoNATServicePrivate(t)
	defer hs.Close()
	hc, an := makeAutoNAT(t, hs)
	defer hc.Close()
	defer an.Close()

	// subscribe to AutoNat events
	s, err := hc.EventBus().Subscribe(&event.EvtLocalReachabilityChanged{})
	if err != nil {
		t.Fatalf("failed to subscribe to event EvtLocalReachabilityChanged, err=%s", err)
	}

	status := an.Status()
	if status != network.ReachabilityUnknown {
		t.Fatalf("unexpected NAT status: %d", status)
	}

	connect(t, hs, hc)
	expectEvent(t, s, network.ReachabilityPrivate, 3*time.Second)
}

func TestAutoNATPublic(t *testing.T) {
	hs := makeAutoNATServicePublic(t)
	defer hs.Close()
	hc, an := makeAutoNAT(t, hs)
	defer hc.Close()
	defer an.Close()

	// subscribe to AutoNat events
	s, err := hc.EventBus().Subscribe(&event.EvtLocalReachabilityChanged{})
	if err != nil {
		t.Fatalf("failed to subscribe to event EvtLocalReachabilityChanged, err=%s", err)
	}

	status := an.Status()
	if status != network.ReachabilityUnknown {
		t.Fatalf("unexpected NAT status: %d", status)
	}

	connect(t, hs, hc)
	expectEvent(t, s, network.ReachabilityPublic, 3*time.Second)
}

func TestAutoNATPublictoPrivate(t *testing.T) {
	hs := makeAutoNATServicePublic(t)
	defer hs.Close()
	hc, an := makeAutoNAT(t, hs)
	defer hc.Close()
	defer an.Close()

	// subscribe to AutoNat events
	s, err := hc.EventBus().Subscribe(&event.EvtLocalReachabilityChanged{})
	if err != nil {
		t.Fatalf("failed to subscribe to event EvtLocalRoutabilityPublic, err=%s", err)
	}

	if status := an.Status(); status != network.ReachabilityUnknown {
		t.Fatalf("unexpected NAT status: %d", status)
	}

	connect(t, hs, hc)
	expectEvent(t, s, network.ReachabilityPublic, 3*time.Second)

	hs.SetStreamHandler(AutoNATProto, sayPrivateStreamHandler(t))
	hps := makeAutoNATServicePrivate(t)
	connect(t, hps, hc)
	identifyAsServer(hps, hc)

	expectEvent(t, s, network.ReachabilityPrivate, 3*time.Second)
}

func TestAutoNATIncomingEvents(t *testing.T) {
	hs := makeAutoNATServicePrivate(t)
	defer hs.Close()
	hc, ani := makeAutoNAT(t, hs)
	defer hc.Close()
	defer ani.Close()
	an := ani.(*AmbientAutoNAT)

	status := an.Status()
	if status != network.ReachabilityUnknown {
		t.Fatalf("unexpected NAT status: %d", status)
	}

	connect(t, hs, hc)

	em, _ := hc.EventBus().Emitter(&event.EvtPeerIdentificationCompleted{})
	em.Emit(event.EvtPeerIdentificationCompleted{Peer: hs.ID()})

	require.Eventually(t, func() bool {
		return an.Status() != network.ReachabilityUnknown
	}, 500*time.Millisecond, 10*time.Millisecond, "Expected probe due to identification of autonat service")
}

func TestAutoNATObservationRecording(t *testing.T) {
	hs := makeAutoNATServicePublic(t)
	defer hs.Close()
	hc, ani := makeAutoNAT(t, hs)
	defer hc.Close()
	defer ani.Close()
	an := ani.(*AmbientAutoNAT)

	s, err := hc.EventBus().Subscribe(&event.EvtLocalReachabilityChanged{})
	if err != nil {
		t.Fatalf("failed to subscribe to event EvtLocalRoutabilityPublic, err=%s", err)
	}

	// pubic observation without address should be ignored.
	an.recordObservation(autoNATResult{network.ReachabilityPublic, nil})
	if an.Status() != network.ReachabilityUnknown {
		t.Fatalf("unexpected transition")
	}

	select {
	case <-s.Out():
		t.Fatal("not expecting a public reachability event")
	default:
		// expected
	}

	addr, _ := ma.NewMultiaddr("/ip4/127.0.0.1/udp/1234")
	an.recordObservation(autoNATResult{network.ReachabilityPublic, addr})
	if an.Status() != network.ReachabilityPublic {
		t.Fatalf("failed to transition to public.")
	}

	expectEvent(t, s, network.ReachabilityPublic, 3*time.Second)

	// a single recording should have confidence still at 0, and transition to private quickly.
	an.recordObservation(autoNATResult{network.ReachabilityPrivate, nil})
	if an.Status() != network.ReachabilityPrivate {
		t.Fatalf("failed to transition to private.")
	}

	expectEvent(t, s, network.ReachabilityPrivate, 3*time.Second)

	// stronger public confidence should be harder to undo.
	an.recordObservation(autoNATResult{network.ReachabilityPublic, addr})
	an.recordObservation(autoNATResult{network.ReachabilityPublic, addr})
	if an.Status() != network.ReachabilityPublic {
		t.Fatalf("failed to transition to public.")
	}

	expectEvent(t, s, network.ReachabilityPublic, 3*time.Second)

	an.recordObservation(autoNATResult{network.ReachabilityPrivate, nil})
	if an.Status() != network.ReachabilityPublic {
		t.Fatalf("too-extreme private transition.")
	}

}

func TestStaticNat(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := bhost.NewBlankHost(swarmt.GenSwarm(t))
	defer h.Close()
	s, _ := h.EventBus().Subscribe(&event.EvtLocalReachabilityChanged{})

	nat, err := New(h, WithReachability(network.ReachabilityPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if nat.Status() != network.ReachabilityPrivate {
		t.Fatalf("should be private")
	}
	expectEvent(t, s, network.ReachabilityPrivate, 3*time.Second)
}
