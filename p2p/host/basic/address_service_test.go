package basichost

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
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
			//nat mapping fails, obs addresses added
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
			//nat addr added, obs address added with nat provided port
			Listen: tcpListenAddr,
			Nat:    ma.StringCast("/ip4/100.100.1.1/tcp/100"),
			ObsAddrFunc: func(a ma.Multiaddr) []ma.Multiaddr {
				ip, _ := ma.SplitFirst(a)
				if ip == nil {
					return nil
				}
				if ip.Equal(if1) {
					return []ma.Multiaddr{ma.StringCast("/ip4/2.2.2.2/tcp/20")}
				} else {
					return []ma.Multiaddr{ma.StringCast("/ip4/3.3.3.3/tcp/30")}
				}
			},
			Expected: []ma.Multiaddr{
				ma.StringCast("/ip4/100.100.1.1/tcp/100"),
				ma.StringCast("/ip4/2.2.2.2/tcp/100"),
				ma.StringCast("/ip4/3.3.3.3/tcp/100"),
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
			res := appendNATAddrsForListenAddrs(nil,
				tc.Listen, tc.Nat, tc.ObsAddrFunc, ifaceAddrs)
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
	getAddrService := func() *addressService {
		h, err := NewHost(swarmt.GenSwarm(t), &HostOpts{DisableIdentifyAddressDiscovery: true})
		require.NoError(t, err)
		t.Cleanup(func() { h.Close() })

		as := h.addressService
		return as
	}

	t.Run("NAT Address", func(t *testing.T) {
		as := getAddrService()
		as.natmgr = &mockNatManager{
			HasDiscoveredNATFunc: func() bool { return true },
			GetMappingFunc: func(addr ma.Multiaddr) ma.Multiaddr {
				if _, err := addr.ValueForProtocol(ma.P_UDP); err == nil {
					return ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1")
				}
				return nil
			},
		}
		require.Contains(t, as.Addrs(), ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1"))
	})

	t.Run("NAT And Observed Address", func(t *testing.T) {
		as := getAddrService()
		as.natmgr = &mockNatManager{
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
		require.Contains(t, as.Addrs(), ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1"))
		require.Contains(t, as.Addrs(), ma.StringCast("/ip4/2.2.2.2/tcp/1"))
	})
	t.Run("Only Observed Address", func(t *testing.T) {
		as := getAddrService()
		as.natmgr = nil
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
		require.NotContains(t, as.Addrs(), ma.StringCast("/ip4/2.2.2.2/tcp/1"))
		require.Contains(t, as.Addrs(), ma.StringCast("/ip4/3.3.3.3/udp/1/quic-v1"))
	})
	t.Run("Public Addrs Removed When Private", func(t *testing.T) {
		as := getAddrService()
		as.natmgr = nil
		as.observedAddrsService = &mockObservedAddrs{
			OwnObservedAddrsFunc: func() []ma.Multiaddr {
				return []ma.Multiaddr{ma.StringCast("/ip4/3.3.3.3/udp/1/quic-v1")}
			},
		}
		as.reachability = func() network.Reachability {
			return network.ReachabilityPrivate
		}
		relayAddr := ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1/p2p/QmdXGaeGiVA745XorV1jr11RHxB9z4fqykm6xCUPX1aTJo/p2p-circuit")
		as.autoRelayAddrs = func() []ma.Multiaddr {
			return []ma.Multiaddr{relayAddr}
		}
		require.NotContains(t, as.Addrs(), ma.StringCast("/ip4/3.3.3.3/udp/1/quic-v1"))
		require.Contains(t, as.Addrs(), relayAddr)
		require.Contains(t, as.AllAddrs(), ma.StringCast("/ip4/3.3.3.3/udp/1/quic-v1"))
	})

	t.Run("AddressFactory gets relay addresses", func(t *testing.T) {
		as := getAddrService()
		as.natmgr = nil
		as.observedAddrsService = &mockObservedAddrs{
			OwnObservedAddrsFunc: func() []ma.Multiaddr {
				return []ma.Multiaddr{ma.StringCast("/ip4/3.3.3.3/udp/1/quic-v1")}
			},
		}
		as.reachability = func() network.Reachability {
			return network.ReachabilityPrivate
		}
		relayAddr := ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1/p2p/QmdXGaeGiVA745XorV1jr11RHxB9z4fqykm6xCUPX1aTJo/p2p-circuit")
		as.autoRelayAddrs = func() []ma.Multiaddr {
			return []ma.Multiaddr{relayAddr}
		}
		as.addrsFactory = func(addrs []ma.Multiaddr) []ma.Multiaddr {
			for _, a := range addrs {
				if a.Equal(relayAddr) {
					return []ma.Multiaddr{ma.StringCast("/ip4/3.3.3.3/udp/1/quic-v1")}
				}
			}
			return nil
		}
		require.Contains(t, as.Addrs(), ma.StringCast("/ip4/3.3.3.3/udp/1/quic-v1"))
		require.NotContains(t, as.Addrs(), relayAddr)
	})

	t.Run("updates addresses on signaling", func(t *testing.T) {
		as := getAddrService()
		as.natmgr = nil
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
		as.Start()
		require.Contains(t, as.Addrs(), a1)
		require.NotContains(t, as.Addrs(), a2)
		close(updateChan)
		as.SignalAddressChange()
		select {
		case <-as.AddrsUpdated():
			require.Contains(t, as.Addrs(), a2)
			require.NotContains(t, as.Addrs(), a1)
		case <-time.After(2 * time.Second):
			t.Fatal("expected addrs to be updated")
		}
	})
}
