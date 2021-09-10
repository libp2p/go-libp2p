package holepunch_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	circuit "github.com/libp2p/go-libp2p-circuit"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/peerstore"
	"github.com/libp2p/go-libp2p-testing/race"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	holepunch_pb "github.com/libp2p/go-libp2p/p2p/protocol/holepunch/pb"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"
	"github.com/libp2p/go-msgio/protoio"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"

	"github.com/stretchr/testify/require"
)

type mockEventTracer struct {
	mutex  sync.Mutex
	events []*holepunch.Event
}

func (m *mockEventTracer) Trace(evt *holepunch.Event) {
	m.mutex.Lock()
	m.events = append(m.events, evt)
	m.mutex.Unlock()
}

func (m *mockEventTracer) getEvents() []*holepunch.Event {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	// copy the slice
	return append([]*holepunch.Event{}, m.events...)
}

var _ holepunch.EventTracer = &mockEventTracer{}

func TestNoHolePunchIfDirectConnExists(t *testing.T) {
	tr := &mockEventTracer{}
	h1, hps := mkHostWithHolePunchSvc(t, holepunch.WithTracer(tr))
	defer h1.Close()
	h2, _ := mkHostWithHolePunchSvc(t)
	defer h2.Close()
	require.NoError(t, h1.Connect(context.Background(), peer.AddrInfo{
		ID:    h2.ID(),
		Addrs: h2.Addrs(),
	}))
	time.Sleep(50 * time.Millisecond)
	nc1 := len(h1.Network().ConnsToPeer(h2.ID()))
	require.GreaterOrEqual(t, nc1, 1)
	nc2 := len(h2.Network().ConnsToPeer(h1.ID()))
	require.GreaterOrEqual(t, nc2, 1)

	require.NoError(t, hps.DirectConnect(h2.ID()))
	require.Equal(t, len(h1.Network().ConnsToPeer(h2.ID())), nc1)
	require.Equal(t, len(h2.Network().ConnsToPeer(h1.ID())), nc2)
	require.Empty(t, tr.getEvents())
}

func TestDirectDialWorks(t *testing.T) {
	// mark all addresses as public
	cpy := manet.Private4
	manet.Private4 = []*net.IPNet{}
	defer func() { manet.Private4 = cpy }()

	tr := &mockEventTracer{}
	h1, h1ps := mkHostWithHolePunchSvc(t, holepunch.WithTracer(tr))
	defer h1.Close()
	h2, _ := mkHostWithHolePunchSvc(t)
	defer h2.Close()
	h2.RemoveStreamHandler(holepunch.Protocol)
	h1.Peerstore().AddAddrs(h2.ID(), h2.Addrs(), peerstore.ConnectedAddrTTL)

	// try to hole punch without any connection and streams, if it works -> it's a direct connection
	require.Len(t, h1.Network().ConnsToPeer(h2.ID()), 0)
	require.NoError(t, h1ps.DirectConnect(h2.ID()))
	require.GreaterOrEqual(t, len(h1.Network().ConnsToPeer(h2.ID())), 1)
	require.GreaterOrEqual(t, len(h2.Network().ConnsToPeer(h1.ID())), 1)
	events := tr.getEvents()
	require.Len(t, events, 1)
	require.Equal(t, events[0].Type, holepunch.DirectDialEvtT)
}

func TestEndToEndSimConnect(t *testing.T) {
	tr := &mockEventTracer{}
	h1, h2, relay, _ := makeRelayedHosts(t, holepunch.WithTracer(tr), true)
	defer h1.Close()
	defer h2.Close()
	defer relay.Close()

	// wait till a direct connection is complete
	ensureDirectConn(t, h1, h2)
	// ensure no hole-punching streams are open on either side
	ensureNoHolePunchingStream(t, h1, h2)
	events := tr.getEvents()
	require.Len(t, events, 3)
	require.Equal(t, events[0].Type, holepunch.StartHolePunchEvtT)
	require.Equal(t, events[1].Type, holepunch.HolePunchAttemptEvtT)
	require.Equal(t, events[2].Type, holepunch.EndHolePunchEvtT)
}

func TestFailuresOnInitiator(t *testing.T) {
	tcs := map[string]struct {
		rhandler         func(s network.Stream)
		errMsg           string
		holePunchTimeout time.Duration
	}{
		"responder does NOT send a CONNECT message": {
			rhandler: func(s network.Stream) {
				wr := protoio.NewDelimitedWriter(s)
				wr.WriteMsg(&holepunch_pb.HolePunch{Type: holepunch_pb.HolePunch_SYNC.Enum()})
			},
			errMsg: "expect CONNECT message, got SYNC",
		},
		"responder does NOT support protocol": {
			rhandler: nil,
		},
		"unable to READ CONNECT message from responder": {
			rhandler: func(s network.Stream) {
				s.Reset()
			},
			errMsg: "failed to read CONNECT message",
		},
		"responder does NOT reply within hole punch deadline": {
			holePunchTimeout: 10 * time.Millisecond,
			rhandler: func(s network.Stream) {
				time.Sleep(5 * time.Second)
			},
			errMsg: "i/o deadline reached",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			if tc.holePunchTimeout != 0 {
				cpy := holepunch.StreamTimeout
				holepunch.StreamTimeout = tc.holePunchTimeout
				defer func() { holepunch.StreamTimeout = cpy }()
			}

			tr := &mockEventTracer{}
			h1, h2, relay, _ := makeRelayedHosts(t, holepunch.WithTracer(tr), false)
			defer h1.Close()
			defer h2.Close()
			defer relay.Close()
			hps := addHolePunchService(t, h2)

			if tc.rhandler != nil {
				h1.SetStreamHandler(holepunch.Protocol, tc.rhandler)
			} else {
				h1.RemoveStreamHandler(holepunch.Protocol)
			}

			err := hps.DirectConnect(h1.ID())
			require.Error(t, err)
			if tc.errMsg != "" {
				require.Contains(t, err.Error(), tc.errMsg)
			}
		})

	}
}

func TestFailuresOnResponder(t *testing.T) {
	tcs := map[string]struct {
		initiator        func(s network.Stream)
		errMsg           string
		holePunchTimeout time.Duration
	}{
		"initiator does NOT send a CONNECT message": {
			initiator: func(s network.Stream) {
				protoio.NewDelimitedWriter(s).WriteMsg(&holepunch_pb.HolePunch{Type: holepunch_pb.HolePunch_SYNC.Enum()})
			},
			errMsg: "expected CONNECT message",
		},
		"initiator does NOT send a SYNC message after a Connect message": {
			initiator: func(s network.Stream) {
				w := protoio.NewDelimitedWriter(s)
				w.WriteMsg(&holepunch_pb.HolePunch{Type: holepunch_pb.HolePunch_CONNECT.Enum()})
				w.WriteMsg(&holepunch_pb.HolePunch{Type: holepunch_pb.HolePunch_CONNECT.Enum()})
			},
			errMsg: "expected SYNC message",
		},
		"initiator does NOT reply within hole punch deadline": {
			holePunchTimeout: 10 * time.Millisecond,
			initiator: func(s network.Stream) {
				protoio.NewDelimitedWriter(s).WriteMsg(&holepunch_pb.HolePunch{Type: holepunch_pb.HolePunch_CONNECT.Enum()})
				time.Sleep(10 * time.Second)
			},
			errMsg: "i/o deadline reached",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			if tc.holePunchTimeout != 0 {
				cpy := holepunch.StreamTimeout
				holepunch.StreamTimeout = tc.holePunchTimeout
				defer func() { holepunch.StreamTimeout = cpy }()
			}

			tr := &mockEventTracer{}
			h1, h2, relay, _ := makeRelayedHosts(t, holepunch.WithTracer(tr), false)
			defer h1.Close()
			defer h2.Close()
			defer relay.Close()

			s, err := h2.NewStream(context.Background(), h1.ID(), holepunch.Protocol)
			require.NoError(t, err)

			go tc.initiator(s)

			getTracerError := func(tr *mockEventTracer) []string {
				var errs []string
				events := tr.getEvents()
				for _, ev := range events {
					if errEv, ok := ev.Evt.(*holepunch.ProtocolErrorEvt); ok {
						errs = append(errs, errEv.Error)
					}
				}
				return errs
			}

			require.Eventually(t, func() bool { return len(getTracerError(tr)) > 0 }, 5*time.Second, 100*time.Millisecond)
			errs := getTracerError(tr)
			require.Len(t, errs, 1)
			require.Contains(t, errs[0], tc.errMsg)
		})

	}
}

func ensureNoHolePunchingStream(t *testing.T, h1, h2 host.Host) {
	require.Eventually(t, func() bool {
		for _, c := range h1.Network().ConnsToPeer(h2.ID()) {
			for _, s := range c.GetStreams() {
				if s.ID() == string(holepunch.Protocol) {
					return false
				}
			}
		}
		return true
	}, 5*time.Second, 50*time.Millisecond)

	require.Eventually(t, func() bool {
		for _, c := range h2.Network().ConnsToPeer(h1.ID()) {
			for _, s := range c.GetStreams() {
				if s.ID() == string(holepunch.Protocol) {
					return false
				}
			}
		}
		return true
	}, 5*time.Second, 50*time.Millisecond)
}

func ensureDirectConn(t *testing.T, h1, h2 host.Host) {
	require.Eventually(t, func() bool {
		for _, c := range h1.Network().ConnsToPeer(h2.ID()) {
			if _, err := c.RemoteMultiaddr().ValueForProtocol(ma.P_CIRCUIT); err != nil {
				return true
			}
		}
		return false
	}, 5*time.Second, 50*time.Millisecond)

	require.Eventually(t, func() bool {
		for _, c := range h2.Network().ConnsToPeer(h1.ID()) {
			if _, err := c.RemoteMultiaddr().ValueForProtocol(ma.P_CIRCUIT); err != nil {
				return true
			}
		}
		return false
	}, 5*time.Second, 50*time.Millisecond)
}

func mkHostWithStaticAutoRelay(t *testing.T, ctx context.Context, relay host.Host) host.Host {
	if race.WithRace() {
		t.Skip("modifying manet.Private4 is racy")
	}
	pi := peer.AddrInfo{
		ID:    relay.ID(),
		Addrs: relay.Addrs(),
	}

	cpy := manet.Private4
	manet.Private4 = []*net.IPNet{}
	defer func() { manet.Private4 = cpy }()

	h, err := libp2p.New(ctx,
		libp2p.ListenAddrs(ma.StringCast("/ip4/127.0.0.1/tcp/0"), ma.StringCast("/ip6/::1/tcp/0")),
		libp2p.EnableRelay(),
		libp2p.EnableAutoRelay(),
		libp2p.ForceReachabilityPrivate(),
		libp2p.StaticRelays([]peer.AddrInfo{pi}),
	)
	require.NoError(t, err)

	// wait till we have a relay addr
	require.Eventually(t, func() bool {
		for _, a := range h.Addrs() {
			if _, err := a.ValueForProtocol(ma.P_CIRCUIT); err == nil {
				return true
			}
		}
		return false
	}, 5*time.Second, 50*time.Millisecond)
	return h
}

func makeRelayedHosts(t *testing.T, h1Opt holepunch.Option, addHolePuncher bool) (h1, h2, relay host.Host, hps *holepunch.Service) {
	t.Helper()
	h1, _ = mkHostWithHolePunchSvc(t, h1Opt)
	var err error
	relay, err = libp2p.New(context.Background(),
		libp2p.ListenAddrs(ma.StringCast("/ip4/127.0.0.1/tcp/0"), ma.StringCast("/ip6/::1/tcp/0")),
		libp2p.DisableRelay(),
	)
	require.NoError(t, err)

	_, err = circuit.NewRelay(context.Background(), relay, nil, circuit.OptHop)
	require.NoError(t, err)

	h2 = mkHostWithStaticAutoRelay(t, context.Background(), relay)
	if addHolePuncher {
		hps = addHolePunchService(t, h2)
	}

	// h1 has a relay addr
	// h2 should connect to the relay addr
	var raddr ma.Multiaddr
	for _, a := range h2.Addrs() {
		if _, err := a.ValueForProtocol(ma.P_CIRCUIT); err == nil {
			raddr = a
			break
		}
	}
	require.NotEmpty(t, raddr)
	require.NoError(t, h1.Connect(context.Background(), peer.AddrInfo{
		ID:    h2.ID(),
		Addrs: []ma.Multiaddr{raddr},
	}))
	return
}

func addHolePunchService(t *testing.T, h host.Host) *holepunch.Service {
	t.Helper()
	ids, err := identify.NewIDService(h)
	require.NoError(t, err)
	hps, err := holepunch.NewService(h, ids)
	require.NoError(t, err)
	return hps
}

func mkHostWithHolePunchSvc(t *testing.T, opts ...holepunch.Option) (host.Host, *holepunch.Service) {
	t.Helper()
	h, err := libp2p.New(
		context.Background(),
		libp2p.ListenAddrs(ma.StringCast("/ip4/127.0.0.1/tcp/0"), ma.StringCast("/ip6/::1/tcp/0")),
	)
	require.NoError(t, err)
	ids, err := identify.NewIDService(h)
	require.NoError(t, err)
	hps, err := holepunch.NewService(h, ids, opts...)
	require.NoError(t, err)
	return h, hps
}
