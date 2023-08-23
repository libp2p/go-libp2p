package holepunch_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-testing/race"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/proto"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	holepunch_pb "github.com/libp2p/go-libp2p/p2p/protocol/holepunch/pb"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"

	"github.com/libp2p/go-msgio/pbio"
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

type mockMaddrFilter struct {
	filterLocal  func(remoteID peer.ID, maddrs []ma.Multiaddr) []ma.Multiaddr
	filterRemote func(remoteID peer.ID, maddrs []ma.Multiaddr) []ma.Multiaddr
}

func (m mockMaddrFilter) FilterLocal(remoteID peer.ID, maddrs []ma.Multiaddr) []ma.Multiaddr {
	return m.filterLocal(remoteID, maddrs)
}

func (m mockMaddrFilter) FilterRemote(remoteID peer.ID, maddrs []ma.Multiaddr) []ma.Multiaddr {
	return m.filterRemote(remoteID, maddrs)
}

var _ holepunch.AddrFilter = &mockMaddrFilter{}

type mockIDService struct {
	identify.IDService
}

var _ identify.IDService = &mockIDService{}

func newMockIDService(t *testing.T, h host.Host) identify.IDService {
	ids, err := identify.NewIDService(h)
	require.NoError(t, err)
	ids.Start()
	t.Cleanup(func() { ids.Close() })
	return &mockIDService{IDService: ids}
}

func (s *mockIDService) OwnObservedAddrs() []ma.Multiaddr {
	return append(s.IDService.OwnObservedAddrs(), ma.StringCast("/ip4/1.1.1.1/tcp/1234"))
}

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
	if race.WithRace() {
		t.Skip("modifying manet.Private4 is racy")
	}

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
	h1tr := &mockEventTracer{}
	h2tr := &mockEventTracer{}
	h1, h2, relay, _ := makeRelayedHosts(t, []holepunch.Option{holepunch.WithTracer(h1tr)}, []holepunch.Option{holepunch.WithTracer(h2tr)}, true)
	defer h1.Close()
	defer h2.Close()
	defer relay.Close()

	// wait till a direct connection is complete
	ensureDirectConn(t, h1, h2)
	// ensure no hole-punching streams are open on either side
	ensureNoHolePunchingStream(t, h1, h2)
	var h2Events []*holepunch.Event
	require.Eventually(t,
		func() bool {
			h2Events = h2tr.getEvents()
			return len(h2Events) == 3
		},
		time.Second,
		10*time.Millisecond,
	)
	require.Equal(t, holepunch.StartHolePunchEvtT, h2Events[0].Type)
	require.Equal(t, holepunch.HolePunchAttemptEvtT, h2Events[1].Type)
	require.Equal(t, holepunch.EndHolePunchEvtT, h2Events[2].Type)

	h1Events := h1tr.getEvents()
	// We don't really expect a hole-punched connection to be established in this test,
	// as we probably don't get the timing right for the TCP simultaneous open.
	// From time to time, it still happens occasionally, and then we get a EndHolePunchEvtT here.
	if len(h1Events) != 2 && len(h1Events) != 3 {
		t.Fatal("expected either 2 or 3 events")
	}
	require.Equal(t, holepunch.StartHolePunchEvtT, h1Events[0].Type)
	require.Equal(t, holepunch.HolePunchAttemptEvtT, h1Events[1].Type)
	if len(h1Events) == 3 {
		require.Equal(t, holepunch.EndHolePunchEvtT, h1Events[2].Type)
	}
}

func TestFailuresOnInitiator(t *testing.T) {
	tcs := map[string]struct {
		rhandler         func(s network.Stream)
		errMsg           string
		holePunchTimeout time.Duration
		filter           func(remoteID peer.ID, maddrs []ma.Multiaddr) []ma.Multiaddr
	}{
		"responder does NOT send a CONNECT message": {
			rhandler: func(s network.Stream) {
				wr := pbio.NewDelimitedWriter(s)
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
			rhandler:         func(s network.Stream) { time.Sleep(5 * time.Second) },
			errMsg:           "i/o deadline reached",
		},
		"no addrs after filtering": {
			errMsg:   "aborting hole punch initiation as we have no public address",
			rhandler: func(s network.Stream) { time.Sleep(5 * time.Second) },
			filter: func(remoteID peer.ID, maddrs []ma.Multiaddr) []ma.Multiaddr {
				return []ma.Multiaddr{}
			},
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
			h1, h2, relay, _ := makeRelayedHosts(t, nil, nil, false)
			defer h1.Close()
			defer h2.Close()
			defer relay.Close()

			opts := []holepunch.Option{holepunch.WithTracer(tr)}
			if tc.filter != nil {
				f := mockMaddrFilter{
					filterLocal:  tc.filter,
					filterRemote: tc.filter,
				}
				opts = append(opts, holepunch.WithAddrFilter(f))
			}

			hps := addHolePunchService(t, h2, opts...)
			// wait until the hole punching protocol has actually started
			require.Eventually(t, func() bool {
				protos, _ := h2.Peerstore().SupportsProtocols(h1.ID(), holepunch.Protocol)
				return len(protos) > 0
			}, 200*time.Millisecond, 10*time.Millisecond)

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

func addrsToBytes(as []ma.Multiaddr) [][]byte {
	bzs := make([][]byte, 0, len(as))
	for _, a := range as {
		bzs = append(bzs, a.Bytes())
	}
	return bzs
}

func TestFailuresOnResponder(t *testing.T) {
	tcs := map[string]struct {
		initiator        func(s network.Stream)
		errMsg           string
		holePunchTimeout time.Duration
		filter           func(remoteID peer.ID, maddrs []ma.Multiaddr) []ma.Multiaddr
	}{
		"initiator does NOT send a CONNECT message": {
			initiator: func(s network.Stream) {
				pbio.NewDelimitedWriter(s).WriteMsg(&holepunch_pb.HolePunch{Type: holepunch_pb.HolePunch_SYNC.Enum()})
			},
			errMsg: "expected CONNECT message",
		},
		"initiator does NOT send a SYNC message after a CONNECT message": {
			initiator: func(s network.Stream) {
				w := pbio.NewDelimitedWriter(s)
				w.WriteMsg(&holepunch_pb.HolePunch{
					Type:     holepunch_pb.HolePunch_CONNECT.Enum(),
					ObsAddrs: addrsToBytes([]ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/1234")}),
				})
				w.WriteMsg(&holepunch_pb.HolePunch{Type: holepunch_pb.HolePunch_CONNECT.Enum()})
			},
			errMsg: "expected SYNC message",
		},
		"initiator does NOT reply within hole punch deadline": {
			holePunchTimeout: 10 * time.Millisecond,
			initiator: func(s network.Stream) {
				pbio.NewDelimitedWriter(s).WriteMsg(&holepunch_pb.HolePunch{
					Type:     holepunch_pb.HolePunch_CONNECT.Enum(),
					ObsAddrs: addrsToBytes([]ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/1234")}),
				})
				time.Sleep(10 * time.Second)
			},
			errMsg: "i/o deadline reached",
		},
		"initiator does NOT send any addresses in CONNECT": {
			holePunchTimeout: 10 * time.Millisecond,
			initiator: func(s network.Stream) {
				pbio.NewDelimitedWriter(s).WriteMsg(&holepunch_pb.HolePunch{Type: holepunch_pb.HolePunch_CONNECT.Enum()})
				time.Sleep(10 * time.Second)
			},
			errMsg: "expected CONNECT message to contain at least one address",
		},
		"no addrs after filtering": {
			errMsg: "rejecting hole punch request, as we don't have any public addresses",
			initiator: func(s network.Stream) {
				pbio.NewDelimitedWriter(s).WriteMsg(&holepunch_pb.HolePunch{
					Type:     holepunch_pb.HolePunch_CONNECT.Enum(),
					ObsAddrs: addrsToBytes([]ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/1234")}),
				})
				time.Sleep(10 * time.Second)
			},
			filter: func(remoteID peer.ID, maddrs []ma.Multiaddr) []ma.Multiaddr {
				return []ma.Multiaddr{}
			},
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

			opts := []holepunch.Option{holepunch.WithTracer(tr)}
			if tc.filter != nil {
				f := mockMaddrFilter{
					filterLocal:  tc.filter,
					filterRemote: tc.filter,
				}
				opts = append(opts, holepunch.WithAddrFilter(f))
			}

			h1, h2, relay, _ := makeRelayedHosts(t, opts, nil, false)
			defer h1.Close()
			defer h2.Close()
			defer relay.Close()

			s, err := h2.NewStream(network.WithUseTransient(context.Background(), "holepunch"), h1.ID(), holepunch.Protocol)
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

func mkHostWithStaticAutoRelay(t *testing.T, relay host.Host) host.Host {
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

	h, err := libp2p.New(
		libp2p.ListenAddrs(ma.StringCast("/ip4/127.0.0.1/tcp/0")),
		libp2p.EnableRelay(),
		libp2p.EnableAutoRelayWithStaticRelays([]peer.AddrInfo{pi}),
		libp2p.ForceReachabilityPrivate(),
		libp2p.ResourceManager(&network.NullResourceManager{}),
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

func makeRelayedHosts(t *testing.T, h1opt, h2opt []holepunch.Option, addHolePuncher bool) (h1, h2, relay host.Host, hps *holepunch.Service) {
	t.Helper()
	h1, _ = mkHostWithHolePunchSvc(t, h1opt...)
	var err error
	relay, err = libp2p.New(
		libp2p.ListenAddrs(ma.StringCast("/ip4/127.0.0.1/tcp/0")),
		libp2p.DisableRelay(),
		libp2p.ResourceManager(&network.NullResourceManager{}),
	)
	require.NoError(t, err)
	_, err = relayv2.New(relay)
	require.NoError(t, err)

	// make sure the relay service is started and advertised by Identify
	h, err := libp2p.New(
		libp2p.NoListenAddrs,
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.DisableRelay(),
	)
	require.NoError(t, err)
	defer h.Close()
	require.NoError(t, h.Connect(context.Background(), peer.AddrInfo{ID: relay.ID(), Addrs: relay.Addrs()}))
	require.Eventually(t, func() bool {
		supported, err := h.Peerstore().SupportsProtocols(relay.ID(), proto.ProtoIDv2Hop)
		return err == nil && len(supported) > 0
	}, 3*time.Second, 100*time.Millisecond)

	h2 = mkHostWithStaticAutoRelay(t, relay)
	if addHolePuncher {
		hps = addHolePunchService(t, h2, h2opt...)
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

func addHolePunchService(t *testing.T, h host.Host, opts ...holepunch.Option) *holepunch.Service {
	t.Helper()
	hps, err := holepunch.NewService(h, newMockIDService(t, h), opts...)
	require.NoError(t, err)
	return hps
}

func mkHostWithHolePunchSvc(t *testing.T, opts ...holepunch.Option) (host.Host, *holepunch.Service) {
	t.Helper()
	h, err := libp2p.New(
		libp2p.ListenAddrs(ma.StringCast("/ip4/127.0.0.1/tcp/0"), ma.StringCast("/ip6/::1/tcp/0")),
		libp2p.ForceReachabilityPrivate(),
		libp2p.ResourceManager(&network.NullResourceManager{}),
	)
	require.NoError(t, err)
	hps, err := holepunch.NewService(h, newMockIDService(t, h), opts...)
	require.NoError(t, err)
	return h, hps
}
