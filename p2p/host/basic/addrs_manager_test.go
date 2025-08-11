package basichost

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-libp2p/p2p/protocol/autonatv2"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multiaddr/matest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockNatManager struct {
	GetMappingFunc func(addr ma.Multiaddr) ma.Multiaddr
}

func (*mockNatManager) Close() error {
	return nil
}

func (m *mockNatManager) GetMapping(addr ma.Multiaddr) ma.Multiaddr {
	if m.GetMappingFunc == nil {
		return nil
	}
	return m.GetMappingFunc(addr)
}

func (*mockNatManager) HasDiscoveredNAT() bool {
	return true
}

var _ NATManager = &mockNatManager{}

type mockObservedAddrs struct {
	AddrsFunc    func() []ma.Multiaddr
	AddrsForFunc func(ma.Multiaddr) []ma.Multiaddr
}

func (m *mockObservedAddrs) Addrs(int) []ma.Multiaddr { return m.AddrsFunc() }

func (m *mockObservedAddrs) AddrsFor(local ma.Multiaddr) []ma.Multiaddr { return m.AddrsForFunc(local) }

var _ ObservedAddrsManager = &mockObservedAddrs{}

type addrsManagerArgs struct {
	NATManager           NATManager
	AddrsFactory         AddrsFactory
	ObservedAddrsManager ObservedAddrsManager
	ListenAddrs          func() []ma.Multiaddr
	AddCertHashes        func([]ma.Multiaddr) []ma.Multiaddr
	AutoNATClient        autonatv2Client
	Bus                  event.Bus
}

type addrsManagerTestCase struct {
	*addrsManager
	PushRelay        func(relayAddrs []ma.Multiaddr)
	PushReachability func(rch network.Reachability)
}

func newAddrsManagerTestCase(tb testing.TB, args addrsManagerArgs) addrsManagerTestCase {
	eb := args.Bus
	if eb == nil {
		eb = eventbus.NewBus()
	}
	if args.AddrsFactory == nil {
		args.AddrsFactory = func(addrs []ma.Multiaddr) []ma.Multiaddr { return addrs }
	}
	addrsUpdatedChan := make(chan struct{}, 1)

	addCertHashes := func(addrs []ma.Multiaddr) []ma.Multiaddr {
		return addrs
	}
	if args.AddCertHashes != nil {
		addCertHashes = args.AddCertHashes
	}
	am, err := newAddrsManager(
		eb,
		args.NATManager,
		args.AddrsFactory,
		args.ListenAddrs,
		addCertHashes,
		args.ObservedAddrsManager,
		addrsUpdatedChan,
		args.AutoNATClient,
		true,
		prometheus.DefaultRegisterer,
	)
	require.NoError(tb, err)

	require.NoError(tb, am.Start())
	raEm, err := eb.Emitter(new(event.EvtAutoRelayAddrsUpdated), eventbus.Stateful)
	require.NoError(tb, err)

	rchEm, err := eb.Emitter(new(event.EvtLocalReachabilityChanged), eventbus.Stateful)
	require.NoError(tb, err)

	tb.Cleanup(am.Close)
	return addrsManagerTestCase{
		addrsManager: am,
		PushRelay: func(relayAddrs []ma.Multiaddr) {
			err := raEm.Emit(event.EvtAutoRelayAddrsUpdated{RelayAddrs: relayAddrs})
			require.NoError(tb, err)
		},
		PushReachability: func(rch network.Reachability) {
			err := rchEm.Emit(event.EvtLocalReachabilityChanged{Reachability: rch})
			require.NoError(tb, err)
		},
	}
}

func TestAddrsManager(t *testing.T) {
	lhquic := ma.StringCast("/ip4/127.0.0.1/udp/1/quic-v1")
	lhtcp := ma.StringCast("/ip4/127.0.0.1/tcp/1")

	publicQUIC := ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1")
	publicQUIC2 := ma.StringCast("/ip4/1.2.3.4/udp/2/quic-v1")
	publicTCP := ma.StringCast("/ip4/1.2.3.4/tcp/1")
	privQUIC := ma.StringCast("/ip4/100.100.100.101/udp/1/quic-v1")

	t.Run("only nat", func(t *testing.T) {
		am := newAddrsManagerTestCase(t, addrsManagerArgs{
			NATManager: &mockNatManager{
				GetMappingFunc: func(addr ma.Multiaddr) ma.Multiaddr {
					if _, err := addr.ValueForProtocol(ma.P_UDP); err == nil {
						return publicQUIC
					}
					return nil
				},
			},
			ListenAddrs: func() []ma.Multiaddr { return []ma.Multiaddr{lhquic, lhtcp} },
		})
		am.updateAddrsSync()
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			expected := []ma.Multiaddr{publicQUIC, lhquic, lhtcp}
			assert.ElementsMatch(collect, am.Addrs(), expected, "%s\n%s", am.Addrs(), expected)
		}, 5*time.Second, 50*time.Millisecond)
	})

	t.Run("nat and observed addrs", func(t *testing.T) {
		// nat mapping for udp, observed addrs for tcp
		am := newAddrsManagerTestCase(t, addrsManagerArgs{
			NATManager: &mockNatManager{
				GetMappingFunc: func(addr ma.Multiaddr) ma.Multiaddr {
					if _, err := addr.ValueForProtocol(ma.P_UDP); err == nil {
						return privQUIC
					}
					return nil
				},
			},
			ObservedAddrsManager: &mockObservedAddrs{
				AddrsForFunc: func(addr ma.Multiaddr) []ma.Multiaddr {
					if _, err := addr.ValueForProtocol(ma.P_TCP); err == nil {
						return []ma.Multiaddr{publicTCP}
					}
					if _, err := addr.ValueForProtocol(ma.P_UDP); err == nil {
						return []ma.Multiaddr{publicQUIC2}
					}
					return nil
				},
			},
			ListenAddrs: func() []ma.Multiaddr { return []ma.Multiaddr{lhquic, lhtcp} },
		})
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			expected := []ma.Multiaddr{lhquic, lhtcp, privQUIC, publicTCP, publicQUIC2}
			assert.ElementsMatch(collect, am.Addrs(), expected, "%s\n%s", am.Addrs(), expected)
		}, 5*time.Second, 50*time.Millisecond)
	})
	t.Run("nat returns unspecified addr", func(t *testing.T) {
		quicPort1 := ma.StringCast("/ip4/3.3.3.3/udp/1/quic-v1")
		// port from nat, IP from observed addr
		am := newAddrsManagerTestCase(t, addrsManagerArgs{
			NATManager: &mockNatManager{
				GetMappingFunc: func(addr ma.Multiaddr) ma.Multiaddr {
					if addr.Equal(lhquic) {
						return ma.StringCast("/ip4/0.0.0.0/udp/2/quic-v1")
					}
					return nil
				},
			},
			ObservedAddrsManager: &mockObservedAddrs{
				AddrsForFunc: func(addr ma.Multiaddr) []ma.Multiaddr {
					if addr.Equal(lhquic) {
						return []ma.Multiaddr{quicPort1}
					}
					return nil
				},
			},
			ListenAddrs: func() []ma.Multiaddr { return []ma.Multiaddr{lhquic} },
		})
		expected := []ma.Multiaddr{lhquic, quicPort1}
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			assert.ElementsMatch(collect, am.Addrs(), expected, "%s\n%s", am.Addrs(), expected)
		}, 5*time.Second, 50*time.Millisecond)
	})
	t.Run("only observed addrs", func(t *testing.T) {
		am := newAddrsManagerTestCase(t, addrsManagerArgs{
			ObservedAddrsManager: &mockObservedAddrs{
				AddrsForFunc: func(addr ma.Multiaddr) []ma.Multiaddr {
					if addr.Equal(lhtcp) {
						return []ma.Multiaddr{publicTCP}
					}
					if addr.Equal(lhquic) {
						return []ma.Multiaddr{publicQUIC}
					}
					return nil
				},
			},
			ListenAddrs: func() []ma.Multiaddr { return []ma.Multiaddr{lhquic, lhtcp} },
		})
		am.updateAddrsSync()
		expected := []ma.Multiaddr{lhquic, lhtcp, publicTCP, publicQUIC}
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			assert.ElementsMatch(collect, am.Addrs(), expected, "%s\n%s", am.Addrs(), expected)
		}, 5*time.Second, 50*time.Millisecond)
	})

	t.Run("observed addrs limit", func(t *testing.T) {
		quicAddrs := []ma.Multiaddr{
			ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1"),
			ma.StringCast("/ip4/1.2.3.4/udp/2/quic-v1"),
			ma.StringCast("/ip4/1.2.3.4/udp/3/quic-v1"),
			ma.StringCast("/ip4/1.2.3.4/udp/4/quic-v1"),
			ma.StringCast("/ip4/1.2.3.4/udp/5/quic-v1"),
			ma.StringCast("/ip4/1.2.3.4/udp/6/quic-v1"),
			ma.StringCast("/ip4/1.2.3.4/udp/7/quic-v1"),
			ma.StringCast("/ip4/1.2.3.4/udp/8/quic-v1"),
			ma.StringCast("/ip4/1.2.3.4/udp/9/quic-v1"),
			ma.StringCast("/ip4/1.2.3.4/udp/10/quic-v1"),
		}
		am := newAddrsManagerTestCase(t, addrsManagerArgs{
			ObservedAddrsManager: &mockObservedAddrs{
				AddrsForFunc: func(_ ma.Multiaddr) []ma.Multiaddr {
					return quicAddrs
				},
			},
			ListenAddrs: func() []ma.Multiaddr { return []ma.Multiaddr{lhquic} },
		})
		am.updateAddrsSync()
		expected := []ma.Multiaddr{lhquic}
		expected = append(expected, quicAddrs[:maxObservedAddrsPerListenAddr]...)
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			matest.AssertMultiaddrsMatch(collect, expected, am.Addrs())
		}, 2*time.Second, 50*time.Millisecond)
	})
	t.Run("public addrs removed when private", func(t *testing.T) {
		am := newAddrsManagerTestCase(t, addrsManagerArgs{
			ObservedAddrsManager: &mockObservedAddrs{
				AddrsForFunc: func(_ ma.Multiaddr) []ma.Multiaddr {
					return []ma.Multiaddr{publicQUIC}
				},
			},
			ListenAddrs: func() []ma.Multiaddr { return []ma.Multiaddr{lhquic, lhtcp} },
		})

		// remove public addrs
		am.PushReachability(network.ReachabilityPrivate)
		relayAddr := ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1/p2p/QmdXGaeGiVA745XorV1jr11RHxB9z4fqykm6xCUPX1aTJo/p2p-circuit")
		am.PushRelay([]ma.Multiaddr{relayAddr})

		expectedAddrs := []ma.Multiaddr{relayAddr, lhquic, lhtcp}
		expectedAllAddrs := []ma.Multiaddr{publicQUIC, lhquic, lhtcp}
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			assert.ElementsMatch(collect, am.Addrs(), expectedAddrs, "%s\n%s", am.Addrs(), expectedAddrs)
			assert.ElementsMatch(collect, am.DirectAddrs(), expectedAllAddrs, "%s\n%s", am.DirectAddrs(), expectedAllAddrs)
		}, 5*time.Second, 50*time.Millisecond)

		// add public addrs
		am.PushReachability(network.ReachabilityPublic)

		expectedAddrs = expectedAllAddrs
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			assert.ElementsMatch(collect, am.Addrs(), expectedAddrs, "%s\n%s", am.Addrs(), expectedAddrs)
			assert.ElementsMatch(collect, am.DirectAddrs(), expectedAllAddrs, "%s\n%s", am.DirectAddrs(), expectedAllAddrs)
		}, 5*time.Second, 50*time.Millisecond)
	})

	t.Run("addrs factory gets relay addrs", func(t *testing.T) {
		relayAddr := ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1/p2p/QmdXGaeGiVA745XorV1jr11RHxB9z4fqykm6xCUPX1aTJo/p2p-circuit")
		publicQUIC2 := ma.StringCast("/ip4/1.2.3.4/udp/2/quic-v1")
		am := newAddrsManagerTestCase(t, addrsManagerArgs{
			AddrsFactory: func(addrs []ma.Multiaddr) []ma.Multiaddr {
				for _, a := range addrs {
					if a.Equal(relayAddr) {
						return []ma.Multiaddr{publicQUIC2}
					}
				}
				return nil
			},
			ObservedAddrsManager: &mockObservedAddrs{
				AddrsForFunc: func(_ ma.Multiaddr) []ma.Multiaddr {
					return []ma.Multiaddr{publicQUIC}
				},
			},
			ListenAddrs: func() []ma.Multiaddr { return []ma.Multiaddr{lhquic, lhtcp} },
		})
		am.PushReachability(network.ReachabilityPrivate)
		am.PushRelay([]ma.Multiaddr{relayAddr})

		expectedAddrs := []ma.Multiaddr{publicQUIC2}
		expectedAllAddrs := []ma.Multiaddr{publicQUIC, lhquic, lhtcp}
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			assert.ElementsMatch(collect, am.Addrs(), expectedAddrs, "%s\n%s", am.Addrs(), expectedAddrs)
			assert.ElementsMatch(collect, am.DirectAddrs(), expectedAllAddrs, "%s\n%s", am.DirectAddrs(), expectedAllAddrs)
		}, 5*time.Second, 50*time.Millisecond)
	})

	t.Run("updates addresses on signaling", func(t *testing.T) {
		updateChan := make(chan struct{})
		am := newAddrsManagerTestCase(t, addrsManagerArgs{
			AddrsFactory: func(_ []ma.Multiaddr) []ma.Multiaddr {
				select {
				case <-updateChan:
					return []ma.Multiaddr{publicQUIC}
				default:
					return []ma.Multiaddr{publicTCP}
				}
			},
			ListenAddrs: func() []ma.Multiaddr { return []ma.Multiaddr{lhquic, lhtcp} },
		})
		require.Contains(t, am.Addrs(), publicTCP)
		require.NotContains(t, am.Addrs(), publicQUIC)
		close(updateChan)
		am.updateAddrsSync()
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			assert.Contains(collect, am.Addrs(), publicQUIC)
			assert.NotContains(collect, am.Addrs(), publicTCP)
		}, 1*time.Second, 50*time.Millisecond)
	})

	t.Run("addrs factory depends on confirmed addrs", func(t *testing.T) {
		var amp atomic.Pointer[addrsManager]
		q1 := ma.StringCast("/ip4/1.1.1.1/udp/1/quic-v1")
		addrsFactory := func(_ []ma.Multiaddr) []ma.Multiaddr {
			if amp.Load() == nil {
				return nil
			}
			// r is empty as there's no reachability tracker
			r, _, _ := amp.Load().ConfirmedAddrs()
			return append(r, q1)
		}
		am := newAddrsManagerTestCase(t, addrsManagerArgs{
			AddrsFactory: addrsFactory,
			ListenAddrs:  func() []ma.Multiaddr { return []ma.Multiaddr{lhquic, lhtcp} },
		})
		amp.Store(am.addrsManager)
		am.updateAddrsSync()
		matest.AssertEqualMultiaddrs(t, []ma.Multiaddr{q1}, am.Addrs())
	})
}

func TestAddrsManagerReachabilityEvent(t *testing.T) {
	publicQUIC, _ := ma.NewMultiaddr("/ip4/1.2.3.4/udp/1234/quic-v1")
	publicQUIC2, _ := ma.NewMultiaddr("/ip4/1.2.3.4/udp/1235/quic-v1")
	publicTCP, _ := ma.NewMultiaddr("/ip4/1.2.3.4/tcp/1234")

	bus := eventbus.NewBus()

	sub, err := bus.Subscribe(new(event.EvtHostReachableAddrsChanged))
	require.NoError(t, err)
	defer sub.Close()

	am := newAddrsManagerTestCase(t, addrsManagerArgs{
		Bus: bus,
		// currently they aren't being passed to the reachability tracker
		ListenAddrs: func() []ma.Multiaddr { return []ma.Multiaddr{publicQUIC, publicQUIC2, publicTCP} },
		AutoNATClient: mockAutoNATClient{
			F: func(_ context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
				if reqs[0].Addr.Equal(publicQUIC) {
					return autonatv2.Result{Addr: reqs[0].Addr, Idx: 0, Reachability: network.ReachabilityPublic}, nil
				} else if reqs[0].Addr.Equal(publicTCP) || reqs[0].Addr.Equal(publicQUIC2) {
					return autonatv2.Result{Addr: reqs[0].Addr, Idx: 0, Reachability: network.ReachabilityPrivate}, nil
				}
				return autonatv2.Result{}, errors.New("invalid")
			},
		},
	})

	initialUnknownAddrs := []ma.Multiaddr{publicQUIC, publicTCP, publicQUIC2}

	// First event: all addresses are initially unknown
	select {
	case e := <-sub.Out():
		evt := e.(event.EvtHostReachableAddrsChanged)
		require.Empty(t, evt.Reachable)
		require.Empty(t, evt.Unreachable)
		require.ElementsMatch(t, initialUnknownAddrs, evt.Unknown)
	case <-time.After(5 * time.Second):
		t.Fatal("expected initial event for reachability change")
	}

	// Wait for probes to complete and addresses to be classified
	reachableAddrs := []ma.Multiaddr{publicQUIC}
	unreachableAddrs := []ma.Multiaddr{publicTCP, publicQUIC2}
	select {
	case e := <-sub.Out():
		evt := e.(event.EvtHostReachableAddrsChanged)
		require.ElementsMatch(t, reachableAddrs, evt.Reachable)
		require.ElementsMatch(t, unreachableAddrs, evt.Unreachable)
		require.Empty(t, evt.Unknown)
		reachable, unreachable, unknown := am.ConfirmedAddrs()
		require.ElementsMatch(t, reachable, reachableAddrs)
		require.ElementsMatch(t, unreachable, unreachableAddrs)
		require.Empty(t, unknown)
	case <-time.After(5 * time.Second):
		t.Fatal("expected final event for reachability change after probing")
	}
}

func TestRemoveIfNotInSource(t *testing.T) {
	var addrs []ma.Multiaddr
	for i := 0; i < 10; i++ {
		addrs = append(addrs, ma.StringCast(fmt.Sprintf("/ip4/1.2.3.4/tcp/%d", i)))
	}
	slices.SortFunc(addrs, func(a, b ma.Multiaddr) int { return a.Compare(b) })
	cases := []struct {
		addrs    []ma.Multiaddr
		source   []ma.Multiaddr
		expected []ma.Multiaddr
	}{
		{},
		{addrs: slices.Clone(addrs[:5]), source: nil, expected: nil},
		{addrs: nil, source: addrs, expected: nil},
		{addrs: []ma.Multiaddr{addrs[0]}, source: []ma.Multiaddr{addrs[0]}, expected: []ma.Multiaddr{addrs[0]}},
		{addrs: slices.Clone(addrs), source: []ma.Multiaddr{addrs[0]}, expected: []ma.Multiaddr{addrs[0]}},
		{addrs: slices.Clone(addrs), source: slices.Clone(addrs[5:]), expected: slices.Clone(addrs[5:])},
		{addrs: slices.Clone(addrs[:5]), source: []ma.Multiaddr{addrs[0], addrs[2], addrs[8]}, expected: []ma.Multiaddr{addrs[0], addrs[2]}},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			addrs := removeNotInSource(tc.addrs, tc.source)
			require.ElementsMatch(t, tc.expected, addrs, "%s\n%s", tc.expected, tc.addrs)
		})
	}
}

func BenchmarkAreAddrsDifferent(b *testing.B) {
	var addrs [10]ma.Multiaddr
	for i := 0; i < len(addrs); i++ {
		addrs[i] = ma.StringCast(fmt.Sprintf("/ip4/1.1.1.%d/tcp/1", i))
	}
	b.Run("areAddrsDifferent", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			areAddrsDifferent(addrs[:], addrs[:])
		}
	})
}

func BenchmarkRemoveIfNotInSource(b *testing.B) {
	var addrs [10]ma.Multiaddr
	for i := 0; i < len(addrs); i++ {
		addrs[i] = ma.StringCast(fmt.Sprintf("/ip4/1.1.1.%d/tcp/1", i))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		removeNotInSource(slices.Clone(addrs[:5]), addrs[:])
	}
}
