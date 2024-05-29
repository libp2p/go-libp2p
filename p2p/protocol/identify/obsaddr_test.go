package identify

import (
	crand "crypto/rand"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	blankhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/stretchr/testify/require"
)

func newConn(local, remote ma.Multiaddr) *mockConn {
	return &mockConn{local: local, remote: remote}
}

func normalize(addr ma.Multiaddr) ma.Multiaddr {
	for {
		out, last := ma.SplitLast(addr)
		if last == nil {
			return addr
		}
		if _, err := last.ValueForProtocol(ma.P_CERTHASH); err != nil {
			return addr
		}
		addr = out
	}
}

func addrsEqual(a, b []ma.Multiaddr) bool {
	if len(b) != len(a) {
		return false
	}
	for _, x := range b {
		found := false
		for _, y := range a {
			if y.Equal(x) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, x := range a {
		found := false
		for _, y := range b {
			if y.Equal(x) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestObservedAddrManager(t *testing.T) {
	tcp4ListenAddr := ma.StringCast("/ip4/192.168.1.100/tcp/1")
	quic4ListenAddr := ma.StringCast("/ip4/0.0.0.0/udp/1/quic-v1")
	webTransport4ListenAddr := ma.StringCast("/ip4/0.0.0.0/udp/1/quic-v1/webtransport/certhash/uEgNmb28")
	tcp6ListenAddr := ma.StringCast("/ip6/2004::1/tcp/1")
	quic6ListenAddr := ma.StringCast("/ip6/::/udp/1/quic-v1")
	webTransport6ListenAddr := ma.StringCast("/ip6/::/udp/1/quic-v1/webtransport/certhash/uEgNmb28")
	newObservedAddrMgr := func() *ObservedAddrManager {
		listenAddrs := []ma.Multiaddr{
			tcp4ListenAddr, quic4ListenAddr, webTransport4ListenAddr, tcp6ListenAddr, quic6ListenAddr, webTransport6ListenAddr,
		}
		listenAddrsFunc := func() []ma.Multiaddr {
			return listenAddrs
		}
		interfaceListenAddrsFunc := func() ([]ma.Multiaddr, error) {
			return listenAddrs, nil
		}
		o, err := NewObservedAddrManager(listenAddrsFunc, listenAddrsFunc,
			interfaceListenAddrsFunc, normalize)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}

	checkAllEntriesRemoved := func(o *ObservedAddrManager) bool {
		return len(o.Addrs()) == 0 && len(o.externalAddrs) == 0 && len(o.connObservedTWAddrs) == 0 && len(o.localAddrs) == 0
	}
	t.Run("Single Observation", func(t *testing.T) {
		o := newObservedAddrMgr()
		defer o.Close()
		observed := ma.StringCast("/ip4/2.2.2.2/tcp/2")
		c1 := newConn(tcp4ListenAddr, ma.StringCast("/ip4/1.2.3.1/tcp/1"))
		c2 := newConn(tcp4ListenAddr, ma.StringCast("/ip4/1.2.3.2/tcp/1"))
		c3 := newConn(tcp4ListenAddr, ma.StringCast("/ip4/1.2.3.3/tcp/1"))
		c4 := newConn(tcp4ListenAddr, ma.StringCast("/ip4/1.2.3.4/tcp/1"))
		o.Record(c1, observed)
		o.Record(c2, observed)
		o.Record(c3, observed)
		o.Record(c4, observed)
		require.Eventually(t, func() bool {
			return addrsEqual(o.Addrs(), []ma.Multiaddr{observed})
		}, 1*time.Second, 100*time.Millisecond)
		o.removeConn(c1)
		o.removeConn(c2)
		o.removeConn(c3)
		o.removeConn(c4)
		require.Eventually(t, func() bool {
			return checkAllEntriesRemoved(o)
		}, 1*time.Second, 100*time.Millisecond)
	})

	t.Run("WebTransport inferred from QUIC", func(t *testing.T) {
		o := newObservedAddrMgr()
		defer o.Close()
		observedQuic := ma.StringCast("/ip4/2.2.2.2/udp/2/quic-v1")
		observedWebTransport := ma.StringCast("/ip4/2.2.2.2/udp/2/quic-v1/webtransport")
		c1 := newConn(quic4ListenAddr, ma.StringCast("/ip4/1.2.3.1/udp/1/quic-v1"))
		c2 := newConn(quic4ListenAddr, ma.StringCast("/ip4/1.2.3.2/udp/1/quic-v1"))
		c3 := newConn(webTransport4ListenAddr, ma.StringCast("/ip4/1.2.3.3/udp/1/quic-v1/webtransport"))
		c4 := newConn(webTransport4ListenAddr, ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1/webtransport"))
		o.Record(c1, observedQuic)
		o.Record(c2, observedQuic)
		o.Record(c3, observedWebTransport)
		o.Record(c4, observedWebTransport)
		require.Eventually(t, func() bool {
			return addrsEqual(o.Addrs(), []ma.Multiaddr{observedQuic, observedWebTransport})
		}, 1*time.Second, 100*time.Millisecond)
		o.removeConn(c1)
		o.removeConn(c2)
		o.removeConn(c3)
		o.removeConn(c4)
		require.Eventually(t, func() bool {
			return checkAllEntriesRemoved(o)
		}, 1*time.Second, 100*time.Millisecond)
	})

	t.Run("SameObservers", func(t *testing.T) {
		o := newObservedAddrMgr()
		defer o.Close()

		observedQuic := ma.StringCast("/ip4/2.2.2.2/udp/2/quic-v1")

		const N = 4 // ActivationThresh
		var ob1, ob2 [N]connMultiaddrs
		for i := 0; i < N; i++ {
			ob1[i] = newConn(quic4ListenAddr, ma.StringCast(fmt.Sprintf("/ip4/1.2.3.%d/udp/1/quic-v1", i)))
			ob2[i] = newConn(quic4ListenAddr, ma.StringCast(fmt.Sprintf("/ip4/1.2.3.%d/udp/1/quic-v1", i)))
		}
		for i := 0; i < N-1; i++ {
			o.Record(ob1[i], observedQuic)
			o.Record(ob2[i], observedQuic)
		}
		time.Sleep(100 * time.Millisecond)
		require.Equal(t, o.Addrs(), []ma.Multiaddr{})

		// We should have a valid address now
		o.Record(ob1[N-1], observedQuic)
		o.Record(ob2[N-1], observedQuic)
		require.Eventually(t, func() bool {
			return addrsEqual(o.Addrs(), []ma.Multiaddr{observedQuic})
		}, 2*time.Second, 100*time.Millisecond)

		// Now disconnect first observer group
		for i := 0; i < N; i++ {
			o.removeConn(ob1[i])
		}
		time.Sleep(100 * time.Millisecond)
		if !addrsEqual(o.Addrs(), []ma.Multiaddr{observedQuic}) {
			t.Fatalf("address removed too earyly %v %v", o.Addrs(), observedQuic)
		}

		// Now disconnect the second group to check cleanup
		for i := 0; i < N; i++ {
			o.removeConn(ob2[i])
		}
		require.Eventually(t, func() bool {
			return checkAllEntriesRemoved(o)
		}, 2*time.Second, 100*time.Millisecond)
	})
	t.Run("SameObserversDifferentAddrs", func(t *testing.T) {
		o := newObservedAddrMgr()
		defer o.Close()

		observedQuic1 := ma.StringCast("/ip4/2.2.2.2/udp/2/quic-v1")
		observedQuic2 := ma.StringCast("/ip4/2.2.2.2/udp/3/quic-v1")

		const N = 4 // ActivationThresh
		var ob1, ob2 [N]connMultiaddrs
		for i := 0; i < N; i++ {
			ob1[i] = newConn(quic4ListenAddr, ma.StringCast(fmt.Sprintf("/ip4/1.2.3.%d/udp/1/quic-v1", i)))
			ob2[i] = newConn(quic4ListenAddr, ma.StringCast(fmt.Sprintf("/ip4/1.2.3.%d/udp/1/quic-v1", i)))
		}
		for i := 0; i < N-1; i++ {
			o.Record(ob1[i], observedQuic1)
			o.Record(ob2[i], observedQuic2)
		}
		time.Sleep(100 * time.Millisecond)
		require.Equal(t, o.Addrs(), []ma.Multiaddr{})

		// We should have a valid address now
		o.Record(ob1[N-1], observedQuic1)
		o.Record(ob2[N-1], observedQuic2)
		require.Eventually(t, func() bool {
			return addrsEqual(o.Addrs(), []ma.Multiaddr{observedQuic1, observedQuic2})
		}, 2*time.Second, 100*time.Millisecond)

		// Now disconnect first observer group
		for i := 0; i < N; i++ {
			o.removeConn(ob1[i])
		}
		time.Sleep(100 * time.Millisecond)
		if !addrsEqual(o.Addrs(), []ma.Multiaddr{observedQuic2}) {
			t.Fatalf("address removed too earyly %v %v", o.Addrs(), observedQuic2)
		}

		// Now disconnect the second group to check cleanup
		for i := 0; i < N; i++ {
			o.removeConn(ob2[i])
		}
		require.Eventually(t, func() bool {
			return checkAllEntriesRemoved(o)
		}, 2*time.Second, 100*time.Millisecond)
	})

	t.Run("Old observations discarded", func(t *testing.T) {
		o := newObservedAddrMgr()
		defer o.Close()
		c1 := newConn(quic4ListenAddr, ma.StringCast("/ip4/1.2.3.1/udp/1/quic-v1"))
		c2 := newConn(quic4ListenAddr, ma.StringCast("/ip4/1.2.3.2/udp/1/quic-v1"))
		c3 := newConn(webTransport4ListenAddr, ma.StringCast("/ip4/1.2.3.3/udp/1/quic-v1/webtransport"))
		c4 := newConn(webTransport4ListenAddr, ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1/webtransport"))
		var observedQuic, observedWebTransport ma.Multiaddr
		for i := 0; i < 10; i++ {
			// Change the IP address in each observation
			observedQuic = ma.StringCast(fmt.Sprintf("/ip4/2.2.2.%d/udp/2/quic-v1", i))
			observedWebTransport = ma.StringCast(fmt.Sprintf("/ip4/2.2.2.%d/udp/2/quic-v1/webtransport", i))
			o.Record(c1, observedQuic)
			o.Record(c2, observedQuic)
			o.Record(c3, observedWebTransport)
			o.Record(c4, observedWebTransport)
			time.Sleep(20 * time.Millisecond)
		}

		require.Eventually(t, func() bool {
			return addrsEqual(o.Addrs(), []ma.Multiaddr{observedQuic, observedWebTransport})
		}, 1*time.Second, 100*time.Millisecond)

		tw, err := thinWaistForm(quic4ListenAddr)
		require.NoError(t, err)
		require.Less(t, len(o.externalAddrs[string(tw.TW.Bytes())]), 2)

		require.Equal(t, o.AddrsFor(webTransport4ListenAddr), []ma.Multiaddr{observedWebTransport})
		require.Equal(t, o.AddrsFor(quic4ListenAddr), []ma.Multiaddr{observedQuic})

		o.removeConn(c1)
		o.removeConn(c2)
		o.removeConn(c3)
		o.removeConn(c4)
		require.Eventually(t, func() bool {
			return checkAllEntriesRemoved(o)
		}, 1*time.Second, 100*time.Millisecond)
	})

	t.Run("Many connection many observations", func(t *testing.T) {
		o := newObservedAddrMgr()
		defer o.Close()
		const N = 100
		var tcpConns, quicConns, webTransportConns [N]*mockConn
		for i := 0; i < N; i++ {
			tcpConns[i] = newConn(tcp4ListenAddr, ma.StringCast(fmt.Sprintf("/ip4/1.2.3.%d/tcp/1", i)))
			quicConns[i] = newConn(quic4ListenAddr, ma.StringCast(fmt.Sprintf("/ip4/1.2.3.%d/udp/1/quic-v1", i)))
			webTransportConns[i] = newConn(webTransport4ListenAddr, ma.StringCast(fmt.Sprintf("/ip4/1.2.3.%d/udp/1/quic-v1/webtransport", i)))
		}
		var observedQuic, observedWebTransport, observedTCP ma.Multiaddr
		for i := 0; i < N; i++ {
			for j := 0; j < 5; j++ {
				// ip addr has the form 2.2.<conn-num>.<obs-num>
				observedQuic = ma.StringCast(fmt.Sprintf("/ip4/2.2.%d.%d/udp/2/quic-v1", i/10, j))
				observedWebTransport = ma.StringCast(fmt.Sprintf("/ip4/2.2.%d.%d/udp/2/quic-v1/webtransport", i/10, j))
				observedTCP = ma.StringCast(fmt.Sprintf("/ip4/2.2.%d.%d/tcp/2", i/10, j))
				o.Record(tcpConns[i], observedTCP)
				o.Record(quicConns[i], observedQuic)
				o.Record(webTransportConns[i], observedWebTransport)
				time.Sleep(10 * time.Millisecond)
			}
		}
		// At this point we have 10 groups of N / 10 with 10 observations for every connection
		// The output should remain stable
		require.Eventually(t, func() bool {
			return len(o.Addrs()) == 3*maxExternalThinWaistAddrsPerLocalAddr
		}, 1*time.Second, 100*time.Millisecond)
		addrs := o.Addrs()
		for i := 0; i < 10; i++ {
			require.ElementsMatch(t, o.Addrs(), addrs, "%s %s", o.Addrs(), addrs)
			time.Sleep(10 * time.Millisecond)
		}

		// Now we bias a few address counts and check for sorting correctness
		var resTCPAddrs, resQuicAddrs, resWebTransportAddrs [maxExternalThinWaistAddrsPerLocalAddr]ma.Multiaddr
		for i := 0; i < maxExternalThinWaistAddrsPerLocalAddr; i++ {
			resTCPAddrs[i] = ma.StringCast(fmt.Sprintf("/ip4/2.2.%d.4/tcp/2", 9-i))
			resQuicAddrs[i] = ma.StringCast(fmt.Sprintf("/ip4/2.2.%d.4/udp/2/quic-v1", 9-i))
			resWebTransportAddrs[i] = ma.StringCast(fmt.Sprintf("/ip4/2.2.%d.4/udp/2/quic-v1/webtransport", 9-i))
			o.Record(tcpConns[i], resTCPAddrs[i])
			o.Record(quicConns[i], resQuicAddrs[i])
			o.Record(webTransportConns[i], resWebTransportAddrs[i])
			time.Sleep(10 * time.Millisecond)
		}
		var allAddrs []ma.Multiaddr
		allAddrs = append(allAddrs, resTCPAddrs[:]...)
		allAddrs = append(allAddrs, resQuicAddrs[:]...)
		allAddrs = append(allAddrs, resWebTransportAddrs[:]...)
		require.Eventually(t, func() bool {
			return addrsEqual(o.Addrs(), allAddrs)
		}, 1*time.Second, 100*time.Millisecond)

		for i := 0; i < N; i++ {
			o.removeConn(tcpConns[i])
			o.removeConn(quicConns[i])
			o.removeConn(webTransportConns[i])
		}
		require.Eventually(t, func() bool {
			return checkAllEntriesRemoved(o)
		}, 1*time.Second, 100*time.Millisecond)
	})

	t.Run("WebTransport certhash", func(t *testing.T) {
		o := newObservedAddrMgr()
		observedWebTransport := ma.StringCast("/ip4/2.2.2.2/udp/1/quic-v1/webtransport")
		c1 := newConn(webTransport4ListenAddr, ma.StringCast("/ip4/1.2.3.1/udp/1/quic-v1/webtransport"))
		c2 := newConn(webTransport4ListenAddr, ma.StringCast("/ip4/1.2.3.2/udp/1/quic-v1/webtransport"))
		c3 := newConn(webTransport4ListenAddr, ma.StringCast("/ip4/1.2.3.3/udp/1/quic-v1/webtransport"))
		c4 := newConn(webTransport4ListenAddr, ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1/webtransport"))
		o.Record(c1, observedWebTransport)
		o.Record(c2, observedWebTransport)
		o.Record(c3, observedWebTransport)
		o.Record(c4, observedWebTransport)
		require.Eventually(t, func() bool {
			return addrsEqual(o.Addrs(), []ma.Multiaddr{observedWebTransport})
		}, 1*time.Second, 100*time.Millisecond)
		o.removeConn(c1)
		o.removeConn(c2)
		o.removeConn(c3)
		o.removeConn(c4)
		require.Eventually(t, func() bool {
			return checkAllEntriesRemoved(o)
		}, 1*time.Second, 100*time.Millisecond)
	})

	t.Run("getNATType", func(t *testing.T) {
		o := newObservedAddrMgr()
		defer o.Close()

		observedWebTransport := ma.StringCast("/ip4/2.2.2.2/udp/1/quic-v1/webtransport")
		var udpConns [5 * maxExternalThinWaistAddrsPerLocalAddr]connMultiaddrs
		for i := 0; i < len(udpConns); i++ {
			udpConns[i] = newConn(webTransport4ListenAddr, ma.StringCast(fmt.Sprintf("/ip4/1.2.3.%d/udp/1/quic-v1/webtransport", i)))
			o.Record(udpConns[i], observedWebTransport)
			time.Sleep(10 * time.Millisecond)
		}
		require.Eventually(t, func() bool {
			return addrsEqual(o.Addrs(), []ma.Multiaddr{observedWebTransport})
		}, 1*time.Second, 100*time.Millisecond)

		tcpNAT, udpNAT := o.getNATType()
		require.Equal(t, tcpNAT, network.NATDeviceTypeUnknown)
		require.Equal(t, udpNAT, network.NATDeviceTypeCone)
	})
	t.Run("NATTypeSymmetric", func(t *testing.T) {
		o := newObservedAddrMgr()
		defer o.Close()
		const N = 100
		var tcpConns, quicConns [N]*mockConn
		for i := 0; i < N; i++ {
			tcpConns[i] = newConn(tcp4ListenAddr, ma.StringCast(fmt.Sprintf("/ip4/1.2.3.%d/tcp/1", i)))
			quicConns[i] = newConn(quic4ListenAddr, ma.StringCast(fmt.Sprintf("/ip4/1.2.3.%d/udp/1/quic-v1", i)))
		}
		var observedQuic, observedTCP ma.Multiaddr
		for i := 0; i < N; i++ {
			// ip addr has the form 2.2.<conn-num>.2
			observedQuic = ma.StringCast(fmt.Sprintf("/ip4/2.2.%d.2/udp/2/quic-v1", i%20))
			observedTCP = ma.StringCast(fmt.Sprintf("/ip4/2.2.%d.2/tcp/2", i%20))
			o.Record(tcpConns[i], observedTCP)
			o.Record(quicConns[i], observedQuic)
			time.Sleep(10 * time.Millisecond)
		}
		// At this point we have 20 groups with 5 observations for every connection
		// The output should remain stable
		require.Eventually(t, func() bool {
			return len(o.Addrs()) == 2*maxExternalThinWaistAddrsPerLocalAddr
		}, 1*time.Second, 100*time.Millisecond)

		tcpNAT, udpNAT := o.getNATType()
		require.Equal(t, tcpNAT, network.NATDeviceTypeSymmetric)
		require.Equal(t, udpNAT, network.NATDeviceTypeSymmetric)

		for i := 0; i < N; i++ {
			o.removeConn(tcpConns[i])
			o.removeConn(quicConns[i])
		}
		require.Eventually(t, func() bool {
			return checkAllEntriesRemoved(o)
		}, 1*time.Second, 100*time.Millisecond)
	})
	t.Run("Nill Input", func(t *testing.T) {
		o := newObservedAddrMgr()
		defer o.Close()
		o.maybeRecordObservation(nil, nil)
		remoteAddr := ma.StringCast("/ip4/1.2.3.4/tcp/1")
		o.maybeRecordObservation(newConn(tcp4ListenAddr, remoteAddr), nil)
		o.maybeRecordObservation(nil, remoteAddr)
		o.AddrsFor(nil)
		o.removeConn(nil)
	})

	t.Run("Nat Emitter", func(t *testing.T) {
		o := newObservedAddrMgr()
		defer o.Close()
		bus := eventbus.NewBus()

		s := swarmt.GenSwarm(t, swarmt.EventBus(bus))
		h := blankhost.NewBlankHost(s, blankhost.WithEventBus(bus))
		defer h.Close()
		// make reachability private
		emitter, err := bus.Emitter(new(event.EvtLocalReachabilityChanged), eventbus.Stateful)
		require.NoError(t, err)
		emitter.Emit(event.EvtLocalReachabilityChanged{Reachability: network.ReachabilityPrivate})

		// start nat emitter
		n, err := newNATEmitter(h, o, 10*time.Millisecond)
		require.NoError(t, err)
		defer n.Close()

		sub, err := bus.Subscribe(new(event.EvtNATDeviceTypeChanged))
		require.NoError(t, err)
		observedWebTransport := ma.StringCast("/ip4/2.2.2.2/udp/1/quic-v1/webtransport")
		var udpConns [5 * maxExternalThinWaistAddrsPerLocalAddr]connMultiaddrs
		for i := 0; i < len(udpConns); i++ {
			udpConns[i] = newConn(webTransport4ListenAddr, ma.StringCast(fmt.Sprintf("/ip4/1.2.3.%d/udp/1/quic-v1/webtransport", i)))
			o.Record(udpConns[i], observedWebTransport)
			time.Sleep(10 * time.Millisecond)
		}
		require.Eventually(t, func() bool {
			return addrsEqual(o.Addrs(), []ma.Multiaddr{observedWebTransport})
		}, 1*time.Second, 100*time.Millisecond)

		var e interface{}
		select {
		case e = <-sub.Out():
		case <-time.After(2 * time.Second):
			t.Fatalf("expected NAT change event")
		}
		evt := e.(event.EvtNATDeviceTypeChanged)
		require.Equal(t, evt.TransportProtocol, network.NATTransportUDP)
		require.Equal(t, evt.NatDeviceType, network.NATDeviceTypeCone)
	})
	t.Run("Many connection many observations IP4 And IP6", func(t *testing.T) {
		o := newObservedAddrMgr()
		defer o.Close()
		const N = 100
		var tcp4Conns, quic4Conns, webTransport4Conns [N]*mockConn
		var tcp6Conns, quic6Conns, webTransport6Conns [N]*mockConn
		for i := 0; i < N; i++ {
			tcp4Conns[i] = newConn(tcp4ListenAddr, ma.StringCast(fmt.Sprintf("/ip4/1.2.3.%d/tcp/1", i)))
			quic4Conns[i] = newConn(quic4ListenAddr, ma.StringCast(fmt.Sprintf("/ip4/1.2.3.%d/udp/1/quic-v1", i)))
			webTransport4Conns[i] = newConn(webTransport4ListenAddr, ma.StringCast(fmt.Sprintf("/ip4/1.2.3.%d/udp/1/quic-v1/webtransport", i)))

			tcp6Conns[i] = newConn(tcp6ListenAddr, ma.StringCast(fmt.Sprintf("/ip6/20%02x::/tcp/1", i)))
			quic6Conns[i] = newConn(quic6ListenAddr, ma.StringCast(fmt.Sprintf("/ip6/20%02x::/udp/1/quic-v1", i)))
			webTransport6Conns[i] = newConn(webTransport6ListenAddr, ma.StringCast(fmt.Sprintf("/ip6/20%02x::/udp/1/quic-v1/webtransport", i)))
		}
		var observedQUIC4, observedWebTransport4, observedTCP4 ma.Multiaddr
		var observedQUIC6, observedWebTransport6, observedTCP6 ma.Multiaddr
		for i := 0; i < N; i++ {
			for j := 0; j < 5; j++ {
				// ip addr has the form 2.2.<conn-num>.<obs-num>
				observedQUIC4 = ma.StringCast(fmt.Sprintf("/ip4/2.2.%d.%d/udp/2/quic-v1", i/10, j))
				observedWebTransport4 = ma.StringCast(fmt.Sprintf("/ip4/2.2.%d.%d/udp/2/quic-v1/webtransport", i/10, j))
				observedTCP4 = ma.StringCast(fmt.Sprintf("/ip4/2.2.%d.%d/tcp/2", i/10, j))

				// ip addr has the form 20XX::YY
				observedQUIC6 = ma.StringCast(fmt.Sprintf("/ip6/20%02x::%02x/udp/2/quic-v1", i/10, j))
				observedWebTransport6 = ma.StringCast(fmt.Sprintf("/ip6/20%02x::%02x/udp/2/quic-v1/webtransport", i/10, j))
				observedTCP6 = ma.StringCast(fmt.Sprintf("/ip6/20%02x::%02x/tcp/2", i/10, j))

				o.maybeRecordObservation(tcp4Conns[i], observedTCP4)
				o.maybeRecordObservation(quic4Conns[i], observedQUIC4)
				o.maybeRecordObservation(webTransport4Conns[i], observedWebTransport4)

				o.maybeRecordObservation(tcp6Conns[i], observedTCP6)
				o.maybeRecordObservation(quic6Conns[i], observedQUIC6)
				o.maybeRecordObservation(webTransport6Conns[i], observedWebTransport6)
			}
		}
		// At this point we have 10 groups of N / 10 with 10 observations for every connection
		// The output should remain stable
		require.Eventually(t, func() bool {
			return len(o.Addrs()) == 2*3*maxExternalThinWaistAddrsPerLocalAddr
		}, 1*time.Second, 100*time.Millisecond)
		addrs := o.Addrs()
		for i := 0; i < 10; i++ {
			require.ElementsMatch(t, o.Addrs(), addrs, "%s %s", o.Addrs(), addrs)
			time.Sleep(10 * time.Millisecond)
		}

		// Now we bias a few address counts and check for sorting correctness
		var resTCPAddrs, resQuicAddrs, resWebTransportAddrs []ma.Multiaddr

		for i, idx := 0, 0; i < maxExternalThinWaistAddrsPerLocalAddr; i++ {
			resTCPAddrs = append(resTCPAddrs, ma.StringCast(fmt.Sprintf("/ip4/2.2.%d.4/tcp/2", 9-i)))
			resQuicAddrs = append(resQuicAddrs, ma.StringCast(fmt.Sprintf("/ip4/2.2.%d.4/udp/2/quic-v1", 9-i)))
			resWebTransportAddrs = append(resWebTransportAddrs, ma.StringCast(fmt.Sprintf("/ip4/2.2.%d.4/udp/2/quic-v1/webtransport", 9-i)))

			o.maybeRecordObservation(tcp4Conns[i], resTCPAddrs[idx])
			o.maybeRecordObservation(quic4Conns[i], resQuicAddrs[idx])
			o.maybeRecordObservation(webTransport4Conns[i], resWebTransportAddrs[idx])
			idx++

			resTCPAddrs = append(resTCPAddrs, ma.StringCast(fmt.Sprintf("/ip6/20%02x::04/tcp/2", 9-i)))
			resQuicAddrs = append(resQuicAddrs, ma.StringCast(fmt.Sprintf("/ip6/20%02x::04/udp/2/quic-v1", 9-i)))
			resWebTransportAddrs = append(resWebTransportAddrs, ma.StringCast(fmt.Sprintf("/ip6/20%02x::04/udp/2/quic-v1/webtransport", 9-i)))
			o.maybeRecordObservation(tcp6Conns[i], resTCPAddrs[idx])
			o.maybeRecordObservation(quic6Conns[i], resQuicAddrs[idx])
			o.maybeRecordObservation(webTransport6Conns[i], resWebTransportAddrs[idx])
			idx++
		}
		var allAddrs []ma.Multiaddr
		allAddrs = append(allAddrs, resTCPAddrs[:]...)
		allAddrs = append(allAddrs, resQuicAddrs[:]...)
		allAddrs = append(allAddrs, resWebTransportAddrs[:]...)
		require.Eventually(t, func() bool {
			return addrsEqual(o.Addrs(), allAddrs)
		}, 1*time.Second, 100*time.Millisecond)

		for i := 0; i < N; i++ {
			o.removeConn(tcp4Conns[i])
			o.removeConn(quic4Conns[i])
			o.removeConn(webTransport4Conns[i])
			o.removeConn(tcp6Conns[i])
			o.removeConn(quic6Conns[i])
			o.removeConn(webTransport6Conns[i])
		}
		require.Eventually(t, func() bool {
			return checkAllEntriesRemoved(o)
		}, 1*time.Second, 100*time.Millisecond)
	})
}

func genIPMultiaddr(ip6 bool) ma.Multiaddr {
	var ipB [16]byte
	crand.Read(ipB[:])
	var ip net.IP
	if ip6 {
		ip = net.IP(ipB[:])
	} else {
		ip = net.IP(ipB[:4])
	}
	addr, _ := manet.FromIP(ip)
	return addr
}

func FuzzObservedAddrManager(f *testing.F) {
	protos := []string{
		"/webrtc-direct",
		"/quic-v1",
		"/quic-v1/webtransport",
	}
	tcp4 := ma.StringCast("/ip4/192.168.1.100/tcp/1")
	quic4 := ma.StringCast("/ip4/0.0.0.0/udp/1/quic-v1")
	wt4 := ma.StringCast("/ip4/0.0.0.0/udp/1/quic-v1/webtransport/certhash/uEgNmb28")
	tcp6 := ma.StringCast("/ip6/1::1/tcp/1")
	quic6 := ma.StringCast("/ip6/::/udp/1/quic-v1")
	wt6 := ma.StringCast("/ip6/::/udp/1/quic-v1/webtransport/certhash/uEgNmb28")
	newObservedAddrMgr := func() *ObservedAddrManager {
		listenAddrs := []ma.Multiaddr{
			tcp4, quic4, wt4, tcp6, quic6, wt6,
		}
		listenAddrsFunc := func() []ma.Multiaddr {
			return listenAddrs
		}
		interfaceListenAddrsFunc := func() ([]ma.Multiaddr, error) {
			return listenAddrs, nil
		}
		o, err := NewObservedAddrManager(listenAddrsFunc, listenAddrsFunc,
			interfaceListenAddrsFunc, normalize)
		if err != nil {
			panic(err)
		}
		return o
	}

	f.Fuzz(func(t *testing.T, port uint16) {
		addrs := []ma.Multiaddr{genIPMultiaddr(true), genIPMultiaddr(false)}
		n := len(addrs)
		for i := 0; i < n; i++ {
			addrs = append(addrs, addrs[i].Encapsulate(ma.StringCast(fmt.Sprintf("/tcp/%d", port))))
			addrs = append(addrs, addrs[i].Encapsulate(ma.StringCast(fmt.Sprintf("/udp/%d", port))))
			addrs = append(addrs, ma.StringCast(fmt.Sprintf("/tcp/%d", port)))
			addrs = append(addrs, ma.StringCast(fmt.Sprintf("/udp/%d", port)))
		}
		n = len(addrs)
		for i := 0; i < n; i++ {
			for j := 0; j < len(protos); j++ {
				protoAddr := ma.StringCast(protos[j])
				addrs = append(addrs, addrs[i].Encapsulate(protoAddr))
				addrs = append(addrs, protoAddr)
			}
		}
		o := newObservedAddrMgr()
		defer o.Close()
		for i := 0; i < len(addrs); i++ {
			for _, l := range o.listenAddrs() {
				c := newConn(l, addrs[i])
				o.maybeRecordObservation(c, addrs[i])
				o.maybeRecordObservation(c, nil)
				o.maybeRecordObservation(nil, addrs[i])
				o.removeConn(c)
			}
		}
	})
}

func TestObserver(t *testing.T) {
	tests := []struct {
		addr ma.Multiaddr
		want string
	}{
		{
			addr: ma.StringCast("/ip4/1.2.3.4/tcp/1"),
			want: "1.2.3.4",
		},
		{
			addr: ma.StringCast("/ip4/192.168.0.1/tcp/1"),
			want: "192.168.0.1",
		},
		{
			addr: ma.StringCast("/ip6/200::1/udp/1/quic-v1"),
			want: "200::",
		},
		{
			addr: ma.StringCast("/ip6/::1/udp/1/quic-v1"),
			want: "::",
		},
	}

	for i, tc := range tests {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			got, err := getObserver(tc.addr)
			require.NoError(t, err)
			require.Equal(t, got, tc.want)
		})
	}
}
