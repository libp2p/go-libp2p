package basichost

import (
	"fmt"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppendNATAddrs(t *testing.T) {
	if1, if2 := ma.StringCast("/ip4/192.168.0.100"), ma.StringCast("/ip4/1.1.1.1")
	ifaceAddrs := []ma.Multiaddr{if1, if2}
	tcpListenAddr, udpListenAddr := ma.StringCast("/ip4/0.0.0.0/tcp/1"), ma.StringCast("/ip4/0.0.0.0/udp/2/quic-v1")
	cases := []struct {
		Name        string
		Listen      ma.Multiaddr
		Nat         ma.Multiaddr
		ObsAddrFunc func(ma.Multiaddr) []ma.Multiaddr
		Expected    []ma.Multiaddr
	}{
		{
			Name: "nat map success",
			// nat mapping success, obsaddress ignored
			Listen: ma.StringCast("/ip4/0.0.0.0/udp/1/quic-v1"),
			Nat:    ma.StringCast("/ip4/1.1.1.1/udp/10/quic-v1"),
			ObsAddrFunc: func(m ma.Multiaddr) []ma.Multiaddr {
				return []ma.Multiaddr{ma.StringCast("/ip4/2.2.2.2/udp/100/quic-v1")}
			},
			Expected: []ma.Multiaddr{ma.StringCast("/ip4/1.1.1.1/udp/10/quic-v1")},
		},
		{
			Name: "nat map failure",
			// nat mapping fails, obs addresses added
			Listen: ma.StringCast("/ip4/0.0.0.0/tcp/1"),
			Nat:    nil,
			ObsAddrFunc: func(a ma.Multiaddr) []ma.Multiaddr {
				ipC, _ := ma.SplitFirst(a)
				ip := ipC.Multiaddr()
				switch {
				case ip.Equal(if1):
					return []ma.Multiaddr{ma.StringCast("/ip4/2.2.2.2/tcp/100")}
				case ip.Equal(if2):
					return []ma.Multiaddr{ma.StringCast("/ip4/3.3.3.3/tcp/100")}
				default:
					return []ma.Multiaddr{}
				}
			},
			Expected: []ma.Multiaddr{ma.StringCast("/ip4/2.2.2.2/tcp/100"), ma.StringCast("/ip4/3.3.3.3/tcp/100")},
		},
		{
			Name: "if addrs ignored if not listening on unspecified",
			// nat mapping fails, obs addresses added
			Listen: ma.StringCast("/ip4/192.168.1.1/tcp/1"),
			Nat:    nil,
			ObsAddrFunc: func(a ma.Multiaddr) []ma.Multiaddr {
				ipC, _ := ma.SplitFirst(a)
				ip := ipC.Multiaddr()
				switch {
				case ip.Equal(if1):
					return []ma.Multiaddr{ma.StringCast("/ip4/2.2.2.2/tcp/100")}
				case ip.Equal(if2):
					return []ma.Multiaddr{ma.StringCast("/ip4/3.3.3.3/tcp/100")}
				case ip.Equal(ma.StringCast("/ip4/192.168.1.1")):
					return []ma.Multiaddr{ma.StringCast("/ip4/4.4.4.4/tcp/100")}
				default:
					return []ma.Multiaddr{}
				}
			},
			Expected: []ma.Multiaddr{ma.StringCast("/ip4/4.4.4.4/tcp/100")},
		},
		{
			Name: "nat map success but CGNAT",
			// nat addr added, obs address added with nat provided port
			Listen: tcpListenAddr,
			Nat:    ma.StringCast("/ip4/100.100.1.1/tcp/100"),
			ObsAddrFunc: func(a ma.Multiaddr) []ma.Multiaddr {
				ipC, _ := ma.SplitFirst(a)
				ip := ipC.Multiaddr()
				if ip.Equal(if1) {
					return []ma.Multiaddr{ma.StringCast("/ip4/2.2.2.2/tcp/20")}
				}
				return []ma.Multiaddr{ma.StringCast("/ip4/3.3.3.3/tcp/30")}
			},
			Expected: []ma.Multiaddr{
				ma.StringCast("/ip4/100.100.1.1/tcp/100"),
				ma.StringCast("/ip4/2.2.2.2/tcp/20"),
				ma.StringCast("/ip4/3.3.3.3/tcp/30"),
			},
		},
		{
			Name: "uses unspecified address for obs address",
			// observed address manager should be queries with both specified and unspecified addresses
			// udp observed addresses are mapped to unspecified addresses
			Listen: udpListenAddr,
			Nat:    nil,
			ObsAddrFunc: func(a ma.Multiaddr) []ma.Multiaddr {
				if manet.IsIPUnspecified(a) {
					return []ma.Multiaddr{ma.StringCast("/ip4/3.3.3.3/udp/20/quic-v1")}
				}
				return []ma.Multiaddr{ma.StringCast("/ip4/2.2.2.2/udp/20/quic-v1")}
			},
			Expected: []ma.Multiaddr{
				ma.StringCast("/ip4/2.2.2.2/udp/20/quic-v1"),
				ma.StringCast("/ip4/3.3.3.3/udp/20/quic-v1"),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			as := &addrsManager{
				natManager: &mockNatManager{
					GetMappingFunc: func(addr ma.Multiaddr) ma.Multiaddr {
						return tc.Nat
					},
				},
				observedAddrsManager: &mockObservedAddrs{
					ObservedAddrsForFunc: tc.ObsAddrFunc,
				},
			}
			res := as.appendNATAddrs(nil, []ma.Multiaddr{tc.Listen}, ifaceAddrs)
			res = ma.Unique(res)
			require.ElementsMatch(t, tc.Expected, res, "%s\n%s", tc.Expected, res)
		})
	}
}

type mockNatManager struct {
	GetMappingFunc func(addr ma.Multiaddr) ma.Multiaddr
}

func (m *mockNatManager) Close() error {
	return nil
}

func (m *mockNatManager) GetMapping(addr ma.Multiaddr) ma.Multiaddr {
	if m.GetMappingFunc == nil {
		return nil
	}
	return m.GetMappingFunc(addr)
}

func (m *mockNatManager) HasDiscoveredNAT() bool {
	return true
}

var _ NATManager = &mockNatManager{}

type mockObservedAddrs struct {
	OwnObservedAddrsFunc func() []ma.Multiaddr
	ObservedAddrsForFunc func(ma.Multiaddr) []ma.Multiaddr
}

func (m *mockObservedAddrs) OwnObservedAddrs() []ma.Multiaddr {
	return m.OwnObservedAddrsFunc()
}

func (m *mockObservedAddrs) ObservedAddrsFor(local ma.Multiaddr) []ma.Multiaddr {
	return m.ObservedAddrsForFunc(local)
}

type addrsManagerArgs struct {
	NATManager           NATManager
	AddrsFactory         AddrsFactory
	ObservedAddrsManager observedAddrsManager
	ListenAddrs          func() []ma.Multiaddr
}

type addrsManagerTestCase struct {
	*addrsManager
	PushRelay        func(relayAddrs []ma.Multiaddr)
	PushReachability func(rch network.Reachability)
}

func newAddrsManagerTestCase(t *testing.T, args addrsManagerArgs) addrsManagerTestCase {
	eb := eventbus.NewBus()
	if args.AddrsFactory == nil {
		args.AddrsFactory = func(addrs []ma.Multiaddr) []ma.Multiaddr { return addrs }
	}
	addrsUpdatedChan := make(chan struct{}, 1)
	am, err := newAddrsManager(
		eb, args.NATManager, args.AddrsFactory, args.ListenAddrs, nil, args.ObservedAddrsManager, addrsUpdatedChan,
	)
	require.NoError(t, err)

	require.NoError(t, am.Start())
	raEm, err := eb.Emitter(new(event.EvtAutoRelayAddrsUpdated), eventbus.Stateful)
	require.NoError(t, err)

	rchEm, err := eb.Emitter(new(event.EvtLocalReachabilityChanged), eventbus.Stateful)
	require.NoError(t, err)

	return addrsManagerTestCase{
		addrsManager: am,
		PushRelay: func(relayAddrs []ma.Multiaddr) {
			err := raEm.Emit(event.EvtAutoRelayAddrsUpdated{RelayAddrs: relayAddrs})
			require.NoError(t, err)
		},
		PushReachability: func(rch network.Reachability) {
			err := rchEm.Emit(event.EvtLocalReachabilityChanged{Reachability: rch})
			require.NoError(t, err)
		},
	}
}

func TestAddrsManager(t *testing.T) {
	lhquic := ma.StringCast("/ip4/127.0.0.1/udp/1/quic-v1")
	lhtcp := ma.StringCast("/ip4/127.0.0.1/tcp/1")

	publicQUIC := ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1")
	publicTCP := ma.StringCast("/ip4/1.2.3.4/tcp/1")

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
		am.triggerAddrsUpdate()
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
						return publicQUIC
					}
					return nil
				},
			},
			ObservedAddrsManager: &mockObservedAddrs{
				ObservedAddrsForFunc: func(addr ma.Multiaddr) []ma.Multiaddr {
					if _, err := addr.ValueForProtocol(ma.P_TCP); err == nil {
						return []ma.Multiaddr{publicTCP}
					}
					return nil
				},
			},
			ListenAddrs: func() []ma.Multiaddr { return []ma.Multiaddr{lhquic, lhtcp} },
		})
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			expected := []ma.Multiaddr{lhquic, lhtcp, publicQUIC, publicTCP}
			assert.ElementsMatch(collect, am.Addrs(), expected, "%s\n%s", am.Addrs(), expected)
		}, 5*time.Second, 50*time.Millisecond)
	})
	t.Run("nat returns unspecified addr", func(t *testing.T) {
		quicPort1 := ma.StringCast("/ip4/3.3.3.3/udp/1/quic-v1")
		quicPort2 := ma.StringCast("/ip4/3.3.3.3/udp/2/quic-v1")
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
				ObservedAddrsForFunc: func(addr ma.Multiaddr) []ma.Multiaddr {
					if addr.Equal(lhquic) {
						return []ma.Multiaddr{quicPort1}
					}
					return nil
				},
			},
			ListenAddrs: func() []ma.Multiaddr { return []ma.Multiaddr{lhquic} },
		})
		expected := []ma.Multiaddr{lhquic, quicPort2}
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			assert.ElementsMatch(collect, am.Addrs(), expected, "%s\n%s", am.Addrs(), expected)
		}, 5*time.Second, 50*time.Millisecond)
	})
	t.Run("only observed addrs", func(t *testing.T) {
		am := newAddrsManagerTestCase(t, addrsManagerArgs{
			ObservedAddrsManager: &mockObservedAddrs{
				ObservedAddrsForFunc: func(addr ma.Multiaddr) []ma.Multiaddr {
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
		am.triggerAddrsUpdate()
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
				ObservedAddrsForFunc: func(addr ma.Multiaddr) []ma.Multiaddr {
					return quicAddrs
				},
			},
			ListenAddrs: func() []ma.Multiaddr { return []ma.Multiaddr{lhquic} },
		})
		am.triggerAddrsUpdate()
		expected := []ma.Multiaddr{lhquic}
		expected = append(expected, quicAddrs[:maxObservedAddrsPerListenAddr]...)
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			assert.ElementsMatch(collect, am.Addrs(), expected, "%s\n%s", am.Addrs(), expected)
		}, 5*time.Second, 50*time.Millisecond)
	})
	t.Run("public addrs removed when private", func(t *testing.T) {
		am := newAddrsManagerTestCase(t, addrsManagerArgs{
			ObservedAddrsManager: &mockObservedAddrs{
				ObservedAddrsForFunc: func(addr ma.Multiaddr) []ma.Multiaddr {
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
				ObservedAddrsForFunc: func(addr ma.Multiaddr) []ma.Multiaddr {
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
			AddrsFactory: func(addrs []ma.Multiaddr) []ma.Multiaddr {
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
		am.triggerAddrsUpdate()
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			assert.Contains(collect, am.Addrs(), publicQUIC)
			assert.NotContains(collect, am.Addrs(), publicTCP)
		}, 1*time.Second, 50*time.Millisecond)
	})
}

func BenchmarkAreAddrsDifferent(b *testing.B) {
	var addrs [10]ma.Multiaddr
	for i := 0; i < len(addrs); i++ {
		addrs[i] = ma.StringCast(fmt.Sprintf("/ip4/1.1.1.%d/tcp/1", i))
	}
	am := &addrsManager{}
	b.Run("areAddrsDifferent", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			am.areAddrsDifferent(addrs[:], addrs[:])
		}
	})
}
