package basichost

import (
	"context"
	"crypto/rand"
	"fmt"
	mrand "math/rand/v2"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	"github.com/libp2p/go-libp2p/p2p/protocol/autonatv2"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multiaddr/matest"
	manet "github.com/multiformats/go-multiaddr/net"
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

type addrStoreArgs struct {
	AddrStore               addrStore
	SignKey                 crypto.PrivKey
	HostID                  peer.ID
	DisableSignedPeerRecord bool
}

type addrsManagerArgs struct {
	NATManager                     NATManager
	AddrsFactory                   AddrsFactory
	ObservedAddrsManager           ObservedAddrsManager
	ListenAddrs                    func() []ma.Multiaddr
	AddCertHashes                  func([]ma.Multiaddr) []ma.Multiaddr
	AutoNATClient                  autonatv2Client
	Bus                            event.Bus
	AddrStoreArgs                  addrStoreArgs
	DisableNonPublicAddrPublishing bool
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

	addCertHashes := func(addrs []ma.Multiaddr) []ma.Multiaddr {
		return addrs
	}
	if args.AddCertHashes != nil {
		addCertHashes = args.AddCertHashes
	}
	signKey := args.AddrStoreArgs.SignKey
	addrStore := args.AddrStoreArgs.AddrStore
	pid := args.AddrStoreArgs.HostID
	if args.AddrStoreArgs == (addrStoreArgs{}) {
		var err error
		signKey, _, err = crypto.GenerateEd25519Key(rand.Reader)
		require.NoError(tb, err)
		addrStore, err = pstoremem.NewPeerstore()
		require.NoError(tb, err)
		pid, err = peer.IDFromPrivateKey(signKey)
		require.NoError(tb, err)
	}

	_, isCertified := addrStore.(peerstore.CertifiedAddrBook)
	disableSignedPeerRecord := !isCertified
	am, err := newAddrsManager(
		eb,
		args.NATManager,
		args.AddrsFactory,
		args.ListenAddrs,
		addCertHashes,
		args.ObservedAddrsManager,
		args.AutoNATClient,
		true,
		prometheus.DefaultRegisterer,
		disableSignedPeerRecord,
		args.DisableNonPublicAddrPublishing,
		signKey,
		addrStore,
		pid,
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
				} else if reqs[0].Addr.Equal(publicQUIC2) {
					return autonatv2.Result{Addr: reqs[0].Addr, Idx: 0, Reachability: network.ReachabilityPrivate}, nil
				}
				return autonatv2.Result{Addr: reqs[0].Addr, Idx: 0, Reachability: network.ReachabilityUnknown, AllAddrsRefused: true}, nil
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
	unreachableAddrs := []ma.Multiaddr{publicQUIC2}
	unknownAddrs := []ma.Multiaddr{publicTCP}
	select {
	case e := <-sub.Out():
		evt := e.(event.EvtHostReachableAddrsChanged)
		matest.AssertMultiaddrsMatch(t, reachableAddrs, evt.Reachable)
		matest.AssertMultiaddrsMatch(t, unreachableAddrs, evt.Unreachable)
		matest.AssertMultiaddrsMatch(t, unknownAddrs, evt.Unknown)
		reachable, unreachable, unknown := am.ConfirmedAddrs()
		matest.AssertMultiaddrsMatch(t, reachableAddrs, reachable)
		matest.AssertMultiaddrsMatch(t, unreachableAddrs, unreachable)
		matest.AssertMultiaddrsMatch(t, unknownAddrs, unknown)
		// unreachable addrs should be removed
		matest.AssertMultiaddrsMatch(t, []ma.Multiaddr{publicQUIC, publicTCP}, am.Addrs())
	case <-time.After(5 * time.Second):
		t.Fatal("expected final event for reachability change after probing")
	}
}

func TestAddrsManagerConfirmedAddrsIncludesSecondaryTransports(t *testing.T) {
	// A node listening on every transport kubo enables by default, on two UDP
	// sockets: one reachable, one not. Each UDP bucket interleaves under
	// Multiaddr.Compare (webrtc-direct sorts before its quic-v1 primary), and
	// getConfirmedAddrs and Addrs merge the buckets with helpers that
	// silently drop entries on unsorted input: removeNotInSource must keep
	// all confirmed addrs in both the reachable and unreachable buckets, and
	// removeInSource must remove every confirmed-unreachable addr from Addrs.
	tcp := ma.StringCast("/ip4/1.2.3.4/tcp/4001")
	wsSNI := ma.StringCast("/ip4/1.2.3.4/tcp/4001/tls/sni/*.example.net/ws")
	quicPub := ma.StringCast("/ip4/1.2.3.4/udp/4002/quic-v1")
	webrtcPub := ma.StringCast("/ip4/1.2.3.4/udp/4002/webrtc-direct")
	wtPub := ma.StringCast("/ip4/1.2.3.4/udp/4002/quic-v1/webtransport")
	quicPriv := ma.StringCast("/ip4/1.2.3.4/udp/4001/quic-v1")
	webrtcPriv := ma.StringCast("/ip4/1.2.3.4/udp/4001/webrtc-direct")
	wtPriv := ma.StringCast("/ip4/1.2.3.4/udp/4001/quic-v1/webtransport")
	reachableAddrs := []ma.Multiaddr{tcp, wsSNI, quicPub, webrtcPub, wtPub}
	unreachableAddrs := []ma.Multiaddr{quicPriv, webrtcPriv, wtPriv}
	listenAddrs := append(slices.Clone(reachableAddrs), unreachableAddrs...)

	// The UDP socket on port 4001 is unreachable, everything else is reachable.
	am := newAddrsManagerTestCase(t, addrsManagerArgs{
		ListenAddrs: func() []ma.Multiaddr { return listenAddrs },
		AutoNATClient: mockAutoNATClient{
			F: func(_ context.Context, reqs []autonatv2.Request) (autonatv2.Result, error) {
				rch := network.ReachabilityPublic
				if port, err := reqs[0].Addr.ValueForProtocol(ma.P_UDP); err == nil && port == "4001" {
					rch = network.ReachabilityPrivate
				}
				return autonatv2.Result{Addr: reqs[0].Addr, Idx: 0, Reachability: rch}, nil
			},
		},
	})
	defer am.Close()

	require.Eventually(t, func() bool {
		reachable, unreachable, _ := am.ConfirmedAddrs()
		return len(reachable) == len(reachableAddrs) && len(unreachable) == len(unreachableAddrs)
	}, 5*time.Second, 50*time.Millisecond, "expected all listen addrs to be confirmed")

	reachable, unreachable, unknown := am.ConfirmedAddrs()
	matest.AssertMultiaddrsMatch(t, reachableAddrs, reachable)
	matest.AssertMultiaddrsMatch(t, unreachableAddrs, unreachable)
	require.Empty(t, unknown)
	matest.AssertMultiaddrsMatch(t, reachableAddrs, am.Addrs())
}

func TestAddrsManagerPeerstoreUpdated(t *testing.T) {
	quic1 := ma.StringCast("/ip4/1.2.3.4/udp/1234/quic-v1")
	quic2 := ma.StringCast("/ip4/1.2.3.5/udp/1/quic-v1")

	pstore, err := pstoremem.NewPeerstore()
	require.NoError(t, err)
	cab, _ := peerstore.GetCertifiedAddrBook(pstore)
	signKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	pid, err := peer.IDFromPrivateKey(signKey)
	require.NoError(t, err)

	var update atomic.Bool
	am := newAddrsManagerTestCase(t, addrsManagerArgs{
		ListenAddrs: func() []ma.Multiaddr { return nil },
		AddrsFactory: func([]ma.Multiaddr) []ma.Multiaddr {
			if !update.Load() {
				return []ma.Multiaddr{quic1}
			}
			return []ma.Multiaddr{quic2}
		},
		AddrStoreArgs: addrStoreArgs{
			AddrStore: pstore,
			HostID:    pid,
			SignKey:   signKey,
		},
	})
	defer am.Close()
	matest.AssertEqualMultiaddrs(t, []ma.Multiaddr{quic1}, pstore.Addrs(pid))
	ev := cab.GetPeerRecord(pid)
	pr := peerRecordFromEnvelope(t, ev)
	require.Equal(t, pr.Addrs, []ma.Multiaddr{quic1})
	update.Store(true)
	am.updateAddrsSync()
	matest.AssertEqualMultiaddrs(t, []ma.Multiaddr{quic2}, pstore.Addrs(pid))
	ev = cab.GetPeerRecord(pid)
	pr = peerRecordFromEnvelope(t, ev)
	require.Equal(t, pr.Addrs, []ma.Multiaddr{quic2})

}

func TestAddrsManagerNonPublicAddrPublishing(t *testing.T) {
	publicV4 := ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1")
	publicV6 := ma.StringCast("/ip6/2001:41d0:203:2ca6::/udp/4001/quic-v1")
	loopback4 := ma.StringCast("/ip4/127.0.0.1/udp/1/quic-v1")
	loopback6 := ma.StringCast("/ip6/::1/udp/1/quic-v1")
	rfc1918 := ma.StringCast("/ip4/192.168.1.5/tcp/4001")
	cgnat := ma.StringCast("/ip4/100.64.0.1/tcp/4001")
	linkLocal4 := ma.StringCast("/ip4/169.254.10.10/tcp/4001")
	ula := ma.StringCast("/ip6/fc00::1/tcp/4001")
	linkLocal6 := ma.StringCast("/ip6/fe80::1/tcp/4001")
	reservedV6 := ma.StringCast("/ip6/1e::109d:0:2:c80b/tcp/4001")
	docV6 := ma.StringCast("/ip6/2001:db8::1/tcp/4001")
	circuit := ma.StringCast("/p2p/12D3KooWGyVU3Z7iEFEKnLRWUZSCgZkruxXt9TafKigQv9TUx2N1/p2p-circuit")
	dnsPublic := ma.StringCast("/dns4/example.com/tcp/443/wss")
	dnsLocal := ma.StringCast("/dns4/foo.local/tcp/443")
	zonedLinkLocal6 := ma.StringCast("/ip6zone/eth0/ip6/fe80::1/tcp/4001")

	all := []ma.Multiaddr{
		publicV4, publicV6,
		loopback4, loopback6,
		rfc1918, cgnat, linkLocal4,
		ula, linkLocal6,
		reservedV6, docV6,
		circuit, dnsPublic, dnsLocal,
		zonedLinkLocal6,
	}

	t.Run("publishes everything by default", func(t *testing.T) {
		pstore, err := pstoremem.NewPeerstore()
		require.NoError(t, err)
		cab, _ := peerstore.GetCertifiedAddrBook(pstore)
		signKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
		require.NoError(t, err)
		pid, err := peer.IDFromPrivateKey(signKey)
		require.NoError(t, err)

		am := newAddrsManagerTestCase(t, addrsManagerArgs{
			ListenAddrs:  func() []ma.Multiaddr { return nil },
			AddrsFactory: func([]ma.Multiaddr) []ma.Multiaddr { return all },
			AddrStoreArgs: addrStoreArgs{
				AddrStore: pstore,
				HostID:    pid,
				SignKey:   signKey,
			},
		})
		defer am.Close()

		require.ElementsMatch(t, all, pstore.Addrs(pid))
		pr := peerRecordFromEnvelope(t, cab.GetPeerRecord(pid))
		require.ElementsMatch(t, all, pr.Addrs)
	})

	t.Run("strips non-public IP addrs when publishing is disabled", func(t *testing.T) {
		pstore, err := pstoremem.NewPeerstore()
		require.NoError(t, err)
		cab, _ := peerstore.GetCertifiedAddrBook(pstore)
		signKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
		require.NoError(t, err)
		pid, err := peer.IDFromPrivateKey(signKey)
		require.NoError(t, err)

		am := newAddrsManagerTestCase(t, addrsManagerArgs{
			ListenAddrs:  func() []ma.Multiaddr { return nil },
			AddrsFactory: func([]ma.Multiaddr) []ma.Multiaddr { return all },
			AddrStoreArgs: addrStoreArgs{
				AddrStore: pstore,
				HostID:    pid,
				SignKey:   signKey,
			},
			DisableNonPublicAddrPublishing: true,
		})
		defer am.Close()

		// kept: public v4/v6, /p2p-circuit (no IP), public DNS
		// stripped: loopback, RFC1918, CGNAT, link-local (incl. ip6zone-wrapped), ULA, reserved/doc IPv6, .local DNS
		expected := []ma.Multiaddr{publicV4, publicV6, circuit, dnsPublic}
		require.ElementsMatch(t, expected, pstore.Addrs(pid))
		pr := peerRecordFromEnvelope(t, cab.GetPeerRecord(pid))
		require.ElementsMatch(t, expected, pr.Addrs)
	})
}

func TestRemoveIfNotInSource(t *testing.T) {
	addrs := make([]ma.Multiaddr, 0, 10)
	for i := range 10 {
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
	for i := range len(addrs) {
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
	for i := range len(addrs) {
		addrs[i] = ma.StringCast(fmt.Sprintf("/ip4/1.1.1.%d/tcp/1", i))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		removeNotInSource(slices.Clone(addrs[:5]), addrs[:])
	}
}

// TestAddrsReachabilityStress stress tests addrs reachability logic. It goes through all combinations of
// public, private, and unknown responses for a few public address combinations and asserts that the
// output is expected.
func TestAddrsReachabilityStress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		getAddrs := func(ip string, port int, protocols ...int) []ma.Multiaddr {
			const certHash = "uEiAsGPzpiPGQzSlVHRXrUCT5EkTV7YFrV4VZ3hpEKTd_zg"

			ipProtocol := "ip4"
			if strings.Contains(ip, ":") {
				ipProtocol = "ip6"
			}
			prefix := fmt.Sprintf("/%s/%s", ipProtocol, ip)

			addrs := make([]ma.Multiaddr, 0, len(protocols))
			for _, p := range protocols {
				switch p {
				case ma.P_QUIC_V1:
					addrs = append(addrs, ma.StringCast(fmt.Sprintf("%s/udp/%d/quic-v1", prefix, port)))
				case ma.P_TCP:
					addrs = append(addrs, ma.StringCast(fmt.Sprintf("%s/tcp/%d", prefix, port)))
				case ma.P_WEBTRANSPORT:
					addrs = append(addrs, ma.StringCast(fmt.Sprintf(
						"%s/udp/%d/quic-v1/webtransport/certhash/%s",
						prefix, port, certHash,
					)))
				case ma.P_WEBRTC_DIRECT:
					addrs = append(addrs, ma.StringCast(fmt.Sprintf(
						"%s/udp/%d/webrtc-direct/certhash/%s",
						prefix, port, certHash,
					)))
				case ma.P_WSS:
					addrs = append(addrs, ma.StringCast(fmt.Sprintf("%s/tcp/%d/wss", prefix, port)))
				default:
					t.Fatalf("unsupported protocol: %d", p)
				}
			}
			return addrs
		}

		publicAddrs1 := getAddrs(
			"2.2.2.2",
			8080,
			ma.P_QUIC_V1,
			ma.P_TCP,
			ma.P_WEBTRANSPORT,
			ma.P_WEBRTC_DIRECT,
			ma.P_WSS,
		)
		publicAddrs2 := getAddrs(
			"2001::1",
			8080,
			ma.P_QUIC_V1,
			ma.P_WEBRTC_DIRECT,
			ma.P_WSS,
		)
		privateAddrs1 := getAddrs(
			"192.168.1.1",
			8080,
			ma.P_QUIC_V1,
			ma.P_TCP,
			ma.P_WEBTRANSPORT,
			ma.P_WEBRTC_DIRECT,
			ma.P_WSS,
		)

		allAddrs := slices.Clone(publicAddrs1)
		allAddrs = append(allAddrs, publicAddrs2...)
		allAddrs = append(allAddrs, privateAddrs1...)

		trackedAddrs := slices.DeleteFunc(slices.Clone(allAddrs), func(addr ma.Multiaddr) bool {
			return !manet.IsPublicAddr(addr)
		})
		reachabilities := [...]network.Reachability{
			network.ReachabilityUnknown,
			network.ReachabilityPublic,  // reachable
			network.ReachabilityPrivate, // unreachable
		}
		combinationCount := 1
		for range trackedAddrs {
			combinationCount *= len(reachabilities)
		}

		for combination := range combinationCount {
			reachability := make(map[string]network.Reachability, len(trackedAddrs))
			state := combination
			for _, addr := range trackedAddrs {
				reachability[string(addr.Bytes())] = reachabilities[state%len(reachabilities)]
				state /= len(reachabilities)
			}
			testReachability(t, combination, allAddrs, reachability)
		}
	})
}

type mockAddrStore struct{}

// SetAddrs implements [addrStore].
func (m mockAddrStore) SetAddrs(peer.ID, []ma.Multiaddr, time.Duration) {
}

var _ addrStore = mockAddrStore{}

func testReachability(
	t *testing.T,
	combination int,
	allAddrs []ma.Multiaddr,
	reachability map[string]network.Reachability,
) {
	signKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	pid, err := peer.IDFromPrivateKey(signKey)
	require.NoError(t, err)

	client := mockAutoNATClient{
		func(_ context.Context, requests []autonatv2.Request) (autonatv2.Result, error) {
			x := mrand.IntN(100)
			time.Sleep(time.Duration(x) * time.Millisecond)
			for i, req := range requests {
				if manet.IsPrivateAddr(req.Addr) {
					// the incorrect reply is deliberate the caller should check this
					return autonatv2.Result{
						Addr:         req.Addr,
						Idx:          i,
						Reachability: network.ReachabilityPublic,
					}, nil
				}
				rch := reachability[string(req.Addr.Bytes())]
				switch rch {
				case network.ReachabilityPublic, network.ReachabilityPrivate:
					return autonatv2.Result{
						Addr:         req.Addr,
						Idx:          i,
						Reachability: rch,
					}, nil
				case network.ReachabilityUnknown:
					continue
				default:
					panic(fmt.Sprintf("unexpected reachability: %d", rch))
				}
			}
			return autonatv2.Result{AllAddrsRefused: true}, nil
		},
	}
	am := newAddrsManagerTestCase(t, addrsManagerArgs{
		ListenAddrs:  func() []ma.Multiaddr { return allAddrs },
		AddrsFactory: func(addrs []ma.Multiaddr) []ma.Multiaddr { return addrs },
		AddrStoreArgs: addrStoreArgs{
			AddrStore: mockAddrStore{},
			HostID:    pid,
			SignKey:   signKey,
		},
		AutoNATClient: client,
	})
	defer am.Close()

	time.Sleep(1 * time.Minute)

	hasProtocol := func(addr ma.Multiaddr, protocol int) bool {
		for _, component := range addr {
			if component.Code() == protocol {
				return true
			}
		}
		return false
	}
	effectiveReachability := func(addr ma.Multiaddr) network.Reachability {
		rch, ok := reachability[string(addr.Bytes())]
		if !ok {
			return network.ReachabilityUnknown
		}

		thinWaist, err := thinWaistPart(addr)
		if err != nil {
			return rch
		}
		var primary ma.Multiaddr
		switch {
		case hasProtocol(addr, ma.P_WEBTRANSPORT),
			hasProtocol(addr, ma.P_WEBRTC_DIRECT):
			primary = thinWaist.Encapsulate(ma.StringCast("/quic-v1"))
		case hasProtocol(addr, ma.P_WSS):
			primary = thinWaist
		}
		if primary != nil &&
			reachability[string(primary.Bytes())] == network.ReachabilityPublic {
			rch = network.ReachabilityPublic
		}
		return rch
	}

	var expectedReachable, expectedUnreachable, expectedUnknown []ma.Multiaddr
	for _, addr := range allAddrs {
		if manet.IsPrivateAddr(addr) {
			continue
		}
		rch := effectiveReachability(addr)
		switch rch {
		case network.ReachabilityPublic:
			expectedReachable = append(expectedReachable, addr)
		case network.ReachabilityPrivate:
			expectedUnreachable = append(expectedUnreachable, addr)
		case network.ReachabilityUnknown:
			expectedUnknown = append(expectedUnknown, addr)
		}
	}

	reachable, unreachable, unknown := am.ConfirmedAddrs()
	if !matest.AssertMultiaddrsMatch(t, expectedReachable, reachable) {
		t.Fatalf("failed reachable check for combination: %d", combination)
	}
	if !matest.AssertMultiaddrsMatch(t, expectedUnreachable, unreachable) {
		t.Fatalf("failed unreachable check for combination: %d", combination)
	}
	if !matest.AssertMultiaddrsMatch(t, expectedUnknown, unknown) {
		t.Fatalf("failed unknown check for combination: %d", combination)
	}

	expectedAddrs := slices.DeleteFunc(slices.Clone(allAddrs), func(a ma.Multiaddr) bool {
		rch := effectiveReachability(a)
		return rch == network.ReachabilityPrivate && !manet.IsPrivateAddr(a)
	})
	if !matest.AssertMultiaddrsMatch(t, expectedAddrs, am.Addrs()) {
		t.Fatalf("failed addrs check for combination: %d", combination)
	}
}
