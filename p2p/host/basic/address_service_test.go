package basichost

import (
	"fmt"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"
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
				ip, _ := ma.SplitFirst(a)
				if ip == nil {
					return nil
				}
				if ip.Equal(if1) {
					return []ma.Multiaddr{ma.StringCast("/ip4/2.2.2.2/tcp/100")}
				} else {
					return []ma.Multiaddr{ma.StringCast("/ip4/3.3.3.3/tcp/100")}
				}
			},
			Expected: []ma.Multiaddr{ma.StringCast("/ip4/2.2.2.2/tcp/100"), ma.StringCast("/ip4/3.3.3.3/tcp/100")},
		},
		{
			Name: "nat map success but CGNAT",
			// nat addr added, obs address added with nat provided port
			Listen: tcpListenAddr,
			Nat:    ma.StringCast("/ip4/100.100.1.1/tcp/100"),
			ObsAddrFunc: func(a ma.Multiaddr) []ma.Multiaddr {
				ip, _ := ma.SplitFirst(a)
				if ip == nil {
					return nil
				}
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
			as := &addressManager{
				natManager: &mockNatManager{
					GetMappingFunc: func(addr ma.Multiaddr) ma.Multiaddr {
						return tc.Nat
					},
					HasDiscoveredNATFunc: func() bool { return true },
				},
				observedAddrsService: &mockObservedAddrs{
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
	GetMappingFunc       func(addr ma.Multiaddr) ma.Multiaddr
	HasDiscoveredNATFunc func() bool
}

func (m *mockNatManager) Close() error {
	return nil
}

func (m *mockNatManager) GetMapping(addr ma.Multiaddr) ma.Multiaddr {
	return m.GetMappingFunc(addr)
}

func (m *mockNatManager) HasDiscoveredNAT() bool {
	return m.HasDiscoveredNATFunc()
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

func TestAddressService(t *testing.T) {
	type pushRelayAddrs func(relayAddrs []ma.Multiaddr)
	type pushReachability func(rch network.Reachability)
	getAddrService := func() (*addressManager, pushRelayAddrs, pushReachability) {
		h, err := NewHost(swarmt.GenSwarm(t), &HostOpts{DisableIdentifyAddressDiscovery: true})
		require.NoError(t, err)
		h.Start()
		t.Cleanup(func() { h.Close() })

		rAddrEM, err := h.eventbus.Emitter(new(event.EvtAutoRelayAddrsUpdated), eventbus.Stateful)
		require.NoError(t, err)

		rchEM, err := h.eventbus.Emitter(new(event.EvtLocalReachabilityChanged), eventbus.Stateful)
		require.NoError(t, err)
		return h.addressService, func(relayAddrs []ma.Multiaddr) {
				err := rAddrEM.Emit(event.EvtAutoRelayAddrsUpdated{RelayAddrs: relayAddrs})
				require.NoError(t, err)
			}, func(rch network.Reachability) {
				err := rchEM.Emit(event.EvtLocalReachabilityChanged{Reachability: rch})
				require.NoError(t, err)
			}
	}

	t.Run("NAT Address", func(t *testing.T) {
		as, _, _ := getAddrService()
		as.natManager = &mockNatManager{
			HasDiscoveredNATFunc: func() bool { return true },
			GetMappingFunc: func(addr ma.Multiaddr) ma.Multiaddr {
				if _, err := addr.ValueForProtocol(ma.P_UDP); err == nil {
					return ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1")
				}
				return nil
			},
		}
		as.signalAddressChange()
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			assert.Contains(collect, as.Addrs(), ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1"), "%s\n%s", as.Addrs(), ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1"))
		}, 5*time.Second, 30*time.Millisecond)
	})

	t.Run("NAT And Observed Address", func(t *testing.T) {
		as, _, _ := getAddrService()
		as.natManager = &mockNatManager{
			HasDiscoveredNATFunc: func() bool { return true },
			GetMappingFunc: func(addr ma.Multiaddr) ma.Multiaddr {
				if _, err := addr.ValueForProtocol(ma.P_UDP); err == nil {
					return ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1")
				}
				return nil
			},
		}
		as.observedAddrsService = &mockObservedAddrs{
			ObservedAddrsForFunc: func(addr ma.Multiaddr) []ma.Multiaddr {
				if _, err := addr.ValueForProtocol(ma.P_TCP); err == nil {
					return []ma.Multiaddr{ma.StringCast("/ip4/2.2.2.2/tcp/1")}
				}
				return nil
			},
		}
		as.signalAddressChange()
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			assert.Contains(collect, as.Addrs(), ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1"), "%s\n%s", as.Addrs(), ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1"))
			assert.Contains(collect, as.Addrs(), ma.StringCast("/ip4/2.2.2.2/tcp/1"), "%s\n%s", as.Addrs(), ma.StringCast("/ip4/2.2.2.2/tcp/1"))
		}, 5*time.Second, 30*time.Millisecond)
	})
	t.Run("Only Observed Address", func(t *testing.T) {
		as, _, _ := getAddrService()
		as.natManager = nil
		as.observedAddrsService = &mockObservedAddrs{
			ObservedAddrsForFunc: func(addr ma.Multiaddr) []ma.Multiaddr {
				if _, err := addr.ValueForProtocol(ma.P_TCP); err == nil {
					return []ma.Multiaddr{ma.StringCast("/ip4/2.2.2.2/tcp/1")}
				}
				return nil
			},
			OwnObservedAddrsFunc: func() []ma.Multiaddr {
				return []ma.Multiaddr{ma.StringCast("/ip4/3.3.3.3/udp/1/quic-v1")}
			},
		}
		as.signalAddressChange()
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			assert.NotContains(collect, as.Addrs(), ma.StringCast("/ip4/2.2.2.2/tcp/1"), "%s\n%s", as.Addrs(), ma.StringCast("/ip4/2.2.2.2/tcp/1"))
			assert.Contains(collect, as.Addrs(), ma.StringCast("/ip4/3.3.3.3/udp/1/quic-v1"), "%s\n%s", as.Addrs(), ma.StringCast("/ip4/3.3.3.3/udp/1/quic-v1"))
		}, 5*time.Second, 30*time.Millisecond)
	})
	t.Run("Public Addrs Removed When Private", func(t *testing.T) {
		as, pushRelayAddrs, pushReachability := getAddrService()
		as.natManager = nil
		as.observedAddrsService = &mockObservedAddrs{
			OwnObservedAddrsFunc: func() []ma.Multiaddr {
				return []ma.Multiaddr{ma.StringCast("/ip4/3.3.3.3/udp/1/quic-v1")}
			},
		}
		pushReachability(network.ReachabilityPrivate)
		relayAddr := ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1/p2p/QmdXGaeGiVA745XorV1jr11RHxB9z4fqykm6xCUPX1aTJo/p2p-circuit")
		pushRelayAddrs([]ma.Multiaddr{relayAddr})

		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			assert.NotContains(collect, as.Addrs(), ma.StringCast("/ip4/3.3.3.3/udp/1/quic-v1"), "%s\n%s", as.Addrs(), ma.StringCast("/ip4/3.3.3.3/udp/1/quic-v1"))
			assert.Contains(collect, as.Addrs(), relayAddr, "%s\n%s", as.Addrs(), relayAddr)
			assert.Contains(collect, as.AllAddrs(), ma.StringCast("/ip4/3.3.3.3/udp/1/quic-v1"), "%s\n%s", as.AllAddrs(), ma.StringCast("/ip4/3.3.3.3/udp/1/quic-v1"))
		}, 5*time.Second, 30*time.Millisecond)
	})

	t.Run("AddressFactory gets relay addresses", func(t *testing.T) {
		as, pushRelayAddrs, pushReachability := getAddrService()
		as.natManager = nil
		as.observedAddrsService = &mockObservedAddrs{
			OwnObservedAddrsFunc: func() []ma.Multiaddr {
				return []ma.Multiaddr{ma.StringCast("/ip4/3.3.3.3/udp/1/quic-v1")}
			},
		}
		pushReachability(network.ReachabilityPrivate)
		relayAddr := ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1/p2p/QmdXGaeGiVA745XorV1jr11RHxB9z4fqykm6xCUPX1aTJo/p2p-circuit")
		pushRelayAddrs([]ma.Multiaddr{relayAddr})
		as.addrsFactory = func(addrs []ma.Multiaddr) []ma.Multiaddr {
			for _, a := range addrs {
				if a.Equal(relayAddr) {
					return []ma.Multiaddr{ma.StringCast("/ip4/3.3.3.3/udp/1/quic-v1")}
				}
			}
			return nil
		}
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			assert.Contains(collect, as.Addrs(), ma.StringCast("/ip4/3.3.3.3/udp/1/quic-v1"), "%s\n%s", as.Addrs(), ma.StringCast("/ip4/3.3.3.3/udp/1/quic-v1"))
			assert.NotContains(collect, as.Addrs(), relayAddr, "%s\n%s", as.Addrs(), relayAddr)
		}, 5*time.Second, 30*time.Millisecond)
	})

	t.Run("updates addresses on signaling", func(t *testing.T) {
		as, _, _ := getAddrService()
		as.natManager = nil
		updateChan := make(chan struct{})
		a1 := ma.StringCast("/ip4/1.1.1.1/udp/1/quic-v1")
		a2 := ma.StringCast("/ip4/1.1.1.1/tcp/1")
		as.addrsFactory = func(addrs []ma.Multiaddr) []ma.Multiaddr {
			select {
			case <-updateChan:
				return []ma.Multiaddr{a2}
			default:
				return []ma.Multiaddr{a1}
			}
		}
		require.Contains(t, as.Addrs(), a1)
		require.NotContains(t, as.Addrs(), a2)
		close(updateChan)
		as.signalAddressChange()
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			assert.Contains(collect, as.Addrs(), a2)
			assert.NotContains(collect, as.Addrs(), a1)
		}, 5*time.Second, 30*time.Millisecond)
	})
}

func BenchmarkAreAddrsDifferent(b *testing.B) {
	var addrs [10]ma.Multiaddr
	for i := 0; i < len(addrs); i++ {
		addrs[i] = ma.StringCast(fmt.Sprintf("/ip4/1.1.1.%d/tcp/1", i))
	}
	as := &addressManager{}
	b.Run("areAddrsDifferent", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			as.areAddrsDifferent(addrs[:], addrs[:])
		}
	})
}
