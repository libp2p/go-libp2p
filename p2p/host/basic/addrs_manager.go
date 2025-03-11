package basichost

import (
	"context"
	"fmt"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/host/basic/internal/backoff"
	libp2pwebrtc "github.com/libp2p/go-libp2p/p2p/transport/webrtc"
	libp2pwebtransport "github.com/libp2p/go-libp2p/p2p/transport/webtransport"
	"github.com/libp2p/go-netroute"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

const maxObservedAddrsPerListenAddr = 5

type observedAddrsManager interface {
	OwnObservedAddrs() []ma.Multiaddr
	ObservedAddrsFor(local ma.Multiaddr) []ma.Multiaddr
}

type addrsManager struct {
	eventbus              event.Bus
	natManager            NATManager
	addrsFactory          AddrsFactory
	listenAddrs           func() []ma.Multiaddr
	transportForListening func(ma.Multiaddr) transport.Transport
	observedAddrsManager  observedAddrsManager
	interfaceAddrs        *interfaceAddrsCache

	// triggerAddrsUpdateChan is used to trigger an addresses update.
	triggerAddrsUpdateChan chan struct{}
	// addrsUpdatedChan is notified when addresses change.
	addrsUpdatedChan chan struct{}
	hostReachability atomic.Pointer[network.Reachability]

	addrsMx    sync.RWMutex // protects fields below
	localAddrs []ma.Multiaddr
	relayAddrs []ma.Multiaddr

	wg        sync.WaitGroup
	ctx       context.Context
	ctxCancel context.CancelFunc
}

func newAddrsManager(
	eventbus event.Bus,
	natmgr NATManager,
	addrsFactory AddrsFactory,
	listenAddrs func() []ma.Multiaddr,
	transportForListening func(ma.Multiaddr) transport.Transport,
	observedAddrsManager observedAddrsManager,
	addrsUpdatedChan chan struct{},
) (*addrsManager, error) {
	ctx, cancel := context.WithCancel(context.Background())
	as := &addrsManager{
		eventbus:               eventbus,
		listenAddrs:            listenAddrs,
		transportForListening:  transportForListening,
		observedAddrsManager:   observedAddrsManager,
		natManager:             natmgr,
		addrsFactory:           addrsFactory,
		triggerAddrsUpdateChan: make(chan struct{}, 1),
		addrsUpdatedChan:       addrsUpdatedChan,
		interfaceAddrs:         &interfaceAddrsCache{},
		ctx:                    ctx,
		ctxCancel:              cancel,
	}
	unknownReachability := network.ReachabilityUnknown
	as.hostReachability.Store(&unknownReachability)
	return as, nil
}

func (a *addrsManager) Start() error {
	return a.background()
}

func (a *addrsManager) Close() {
	a.ctxCancel()
	if a.natManager != nil {
		err := a.natManager.Close()
		if err != nil {
			log.Warnf("error closing natmgr: %s", err)
		}
	}
	a.wg.Wait()
}

func (a *addrsManager) NetNotifee() network.Notifiee {
	return &network.NotifyBundle{
		ListenF:      func(network.Network, ma.Multiaddr) { a.triggerAddrsUpdate() },
		ListenCloseF: func(network.Network, ma.Multiaddr) { a.triggerAddrsUpdate() },
	}
}

func (a *addrsManager) triggerAddrsUpdate() {
	// This is ugly, we update here *and* in the background loop, but this ensures the nice property
	// that host.Addrs after host.Network().Listen(...) will return the recently added listen address.
	a.updateLocalAddrs()
	select {
	case a.triggerAddrsUpdateChan <- struct{}{}:
	default:
	}
}

func (a *addrsManager) background() error {
	autoRelayAddrsSub, err := a.eventbus.Subscribe(new(event.EvtAutoRelayAddrsUpdated))
	if err != nil {
		return fmt.Errorf("error subscribing to auto relay addrs: %s", err)
	}

	autonatReachabilitySub, err := a.eventbus.Subscribe(new(event.EvtLocalReachabilityChanged))
	if err != nil {
		return fmt.Errorf("error subscribing to autonat reachability: %s", err)
	}

	// ensure that we have the correct address after returning from Start()
	// update local addrs
	a.updateLocalAddrs()
	// update relay addrs in case we're private
	select {
	case e := <-autoRelayAddrsSub.Out():
		if evt, ok := e.(event.EvtAutoRelayAddrsUpdated); ok {
			a.updateRelayAddrs(evt.RelayAddrs)
		}
	default:
	}
	select {
	case e := <-autonatReachabilitySub.Out():
		if evt, ok := e.(event.EvtLocalReachabilityChanged); ok {
			a.hostReachability.Store(&evt.Reachability)
		}
	default:
	}

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() {
			err := autoRelayAddrsSub.Close()
			if err != nil {
				log.Warnf("error closing auto relay addrs sub: %s", err)
			}
		}()
		defer func() {
			err := autonatReachabilitySub.Close()
			if err != nil {
				log.Warnf("error closing autonat reachability sub: %s", err)
			}
		}()

		ticker := time.NewTicker(addrChangeTickrInterval)
		defer ticker.Stop()
		var prev []ma.Multiaddr
		for {
			a.updateLocalAddrs()
			curr := a.Addrs()
			if a.areAddrsDifferent(prev, curr) {
				log.Debugf("host addresses updated: %s", curr)
				select {
				case a.addrsUpdatedChan <- struct{}{}:
				default:
				}
			}
			prev = curr
			select {
			case <-ticker.C:
			case <-a.triggerAddrsUpdateChan:
			case e := <-autoRelayAddrsSub.Out():
				if evt, ok := e.(event.EvtAutoRelayAddrsUpdated); ok {
					a.updateRelayAddrs(evt.RelayAddrs)
				}
			case e := <-autonatReachabilitySub.Out():
				if evt, ok := e.(event.EvtLocalReachabilityChanged); ok {
					a.hostReachability.Store(&evt.Reachability)
				}
			case <-a.ctx.Done():
				return
			}
		}
	}()
	return nil
}

// Addrs returns the node's dialable addresses both public and private.
// If autorelay is enabled and node reachability is private, it returns
// the node's relay addresses and private network addresses.
func (a *addrsManager) Addrs() []ma.Multiaddr {
	addrs := a.DirectAddrs()
	rch := a.hostReachability.Load()
	if rch != nil && *rch == network.ReachabilityPrivate {
		a.addrsMx.RLock()
		// Delete public addresses if the node's reachability is private, and we have relay addresses
		if len(a.relayAddrs) > 0 {
			addrs = slices.DeleteFunc(addrs, manet.IsPublicAddr)
			addrs = append(addrs, a.relayAddrs...)
		}
		a.addrsMx.RUnlock()
	}
	// Make a copy. Consumers can modify the slice elements
	addrs = slices.Clone(a.addrsFactory(addrs))
	// Add certhashes for the addresses provided by the user via address factory.
	addrs = a.addCertHashes(ma.Unique(addrs))
	slices.SortFunc(addrs, func(a, b ma.Multiaddr) int { return a.Compare(b) })
	return addrs
}

// HolePunchAddrs returns the node's public direct listen addresses for hole punching.
func (a *addrsManager) HolePunchAddrs() []ma.Multiaddr {
	addrs := a.DirectAddrs()
	addrs = slices.Clone(a.addrsFactory(addrs))
	// AllAddrs may ignore observed addresses in favour of NAT mappings.
	// Use both for hole punching.
	if a.observedAddrsManager != nil {
		addrs = append(addrs, a.observedAddrsManager.OwnObservedAddrs()...)
	}
	addrs = ma.Unique(addrs)
	return slices.DeleteFunc(addrs, func(a ma.Multiaddr) bool { return !manet.IsPublicAddr(a) })
}

// DirectAddrs returns all the addresses the host is listening on except circuit addresses.
func (a *addrsManager) DirectAddrs() []ma.Multiaddr {
	a.addrsMx.RLock()
	defer a.addrsMx.RUnlock()
	return slices.Clone(a.localAddrs)
}

func (a *addrsManager) updateRelayAddrs(addrs []ma.Multiaddr) {
	a.addrsMx.Lock()
	defer a.addrsMx.Unlock()
	a.relayAddrs = append(a.relayAddrs[:0], addrs...)
}

var p2pCircuitAddr = ma.StringCast("/p2p-circuit")

func (a *addrsManager) updateLocalAddrs() {
	localAddrs := a.getLocalAddrs()
	slices.SortFunc(localAddrs, func(a, b ma.Multiaddr) int { return a.Compare(b) })

	a.addrsMx.Lock()
	a.localAddrs = localAddrs
	a.addrsMx.Unlock()
}

func (a *addrsManager) getLocalAddrs() []ma.Multiaddr {
	listenAddrs := a.listenAddrs()
	if len(listenAddrs) == 0 {
		return nil
	}

	finalAddrs := make([]ma.Multiaddr, 0, 8)
	finalAddrs = a.appendPrimaryInterfaceAddrs(finalAddrs, listenAddrs)
	finalAddrs = a.appendNATAddrs(finalAddrs, listenAddrs, a.interfaceAddrs.All())

	finalAddrs = ma.Unique(finalAddrs)

	// Remove "/p2p-circuit" addresses from the list.
	// The p2p-circuit listener reports its address as just /p2p-circuit. This is
	// useless for dialing. Users need to manage their circuit addresses themselves,
	// or use AutoRelay.
	finalAddrs = slices.DeleteFunc(finalAddrs, func(a ma.Multiaddr) bool {
		return a.Equal(p2pCircuitAddr)
	})

	// Remove any unspecified address from the list
	finalAddrs = slices.DeleteFunc(finalAddrs, func(a ma.Multiaddr) bool {
		return manet.IsIPUnspecified(a)
	})

	// Add certhashes for /webrtc-direct, /webtransport, etc addresses discovered
	// using identify.
	finalAddrs = a.addCertHashes(finalAddrs)
	return finalAddrs
}

// appendPrimaryInterfaceAddrs appends the primary interface addresses to `dst`.
func (a *addrsManager) appendPrimaryInterfaceAddrs(dst []ma.Multiaddr, listenAddrs []ma.Multiaddr) []ma.Multiaddr {
	// resolving any unspecified listen addressees to use only the primary
	// interface to avoid advertising too many addresses.
	if resolved, err := manet.ResolveUnspecifiedAddresses(listenAddrs, a.interfaceAddrs.Filtered()); err != nil {
		log.Warnw("failed to resolve listen addrs", "error", err)
	} else {
		dst = append(dst, resolved...)
	}
	return dst
}

// appendNATAddrs appends the NAT-ed addrs for the listenAddrs. For unspecified listen addrs it appends the
// public address for all the interfaces.
// Inferring WebTransport from QUIC depends on the observed address manager.
//
// TODO: Merge the natmgr and identify.ObservedAddrManager in to one NatMapper module.
func (a *addrsManager) appendNATAddrs(dst []ma.Multiaddr, listenAddrs []ma.Multiaddr, ifaceAddrs []ma.Multiaddr) []ma.Multiaddr {
	var obsAddrs []ma.Multiaddr
	for _, listenAddr := range listenAddrs {
		var natAddr ma.Multiaddr
		if a.natManager != nil {
			natAddr = a.natManager.GetMapping(listenAddr)
		}

		// The order of the cases below is important.
		switch {
		case natAddr == nil: // no nat mapping
			dst = a.appendObservedAddrs(dst, listenAddr, ifaceAddrs)
		case manet.IsIPUnspecified(natAddr):
			log.Infof("NAT device reported an unspecified IP as it's external address: %s", natAddr)
			_, natRest := ma.SplitFirst(natAddr)
			obsAddrs = a.appendObservedAddrs(obsAddrs[:0], listenAddr, ifaceAddrs)
			for _, addr := range obsAddrs {
				obsIP, _ := ma.SplitFirst(addr)
				if obsIP != nil && manet.IsPublicAddr(obsIP.Multiaddr()) {
					dst = append(dst, obsIP.Encapsulate(natRest))
				}
			}
		// This is !Public as opposed to IsPrivate intentionally.
		// Public is a more restrictive classification in some cases, like IPv6 addresses which only
		// consider unicast IPv6 addresses allocated so far as public(2000::/3).
		case !manet.IsPublicAddr(natAddr): // nat reported non public addr(maybe CGNAT?)
			// use both NAT and observed addr
			dst = append(dst, natAddr)
			dst = a.appendObservedAddrs(dst, listenAddr, ifaceAddrs)
		default: // public addr
			dst = append(dst, natAddr)
		}
	}
	return dst
}

func (a *addrsManager) appendObservedAddrs(dst []ma.Multiaddr, listenAddr ma.Multiaddr, ifaceAddrs []ma.Multiaddr) []ma.Multiaddr {
	if a.observedAddrsManager == nil {
		return dst
	}
	// Add it for the listenAddr first.
	// listenAddr maybe unspecified. That's okay as connections on UDP transports
	// will have the unspecified address as the local address.
	obsAddrs := a.observedAddrsManager.ObservedAddrsFor(listenAddr)
	if len(obsAddrs) > maxObservedAddrsPerListenAddr {
		obsAddrs = obsAddrs[:maxObservedAddrsPerListenAddr]
	}
	dst = append(dst, obsAddrs...)

	// if it can be resolved into more addresses, add them too
	resolved, err := manet.ResolveUnspecifiedAddress(listenAddr, ifaceAddrs)
	if err != nil {
		log.Warnf("failed to resolve listen addr %s, %s: %s", listenAddr, ifaceAddrs, err)
		return dst
	}
	for _, addr := range resolved {
		obsAddrs = a.observedAddrsManager.ObservedAddrsFor(addr)
		if len(obsAddrs) > maxObservedAddrsPerListenAddr {
			obsAddrs = obsAddrs[:maxObservedAddrsPerListenAddr]
		}
		dst = append(dst, obsAddrs...)
	}
	return dst
}

func (a *addrsManager) addCertHashes(addrs []ma.Multiaddr) []ma.Multiaddr {
	if a.transportForListening == nil {
		return addrs
	}

	// TODO(sukunrt): Move this to swarm.
	// There are two parts to determining our external address
	// 1. From the NAT device, or identify, or other such STUN like mechanism.
	// All that matters here is (internal_ip, internal_port, tcp) => (external_ip, external_port, tcp)
	// The rest of the address should be cut and appended to the external one.
	// 2. The user provides us with the address (/ip4/1.2.3.4/udp/1/webrtc-direct) and we add the certhash.
	// This API should be where the transports are, i.e. swarm.
	//
	// It would have been nice to remove this completely and just work with
	// mapping the interface thinwaist addresses (tcp, 192.168.18.18:4000 => 1.2.3.4:4577)
	// but that is only convenient if we're using the same port for listening on
	// all transports which share the same thinwaist protocol. If you listen
	// on 4001 for tcp, and 4002 for websocket, then it's a terrible API.
	type addCertHasher interface {
		AddCertHashes(m ma.Multiaddr) (ma.Multiaddr, bool)
	}

	for i, addr := range addrs {
		wtOK, wtN := libp2pwebtransport.IsWebtransportMultiaddr(addr)
		webrtcOK, webrtcN := libp2pwebrtc.IsWebRTCDirectMultiaddr(addr)
		if (wtOK && wtN == 0) || (webrtcOK && webrtcN == 0) {
			t := a.transportForListening(addr)
			if t == nil {
				continue
			}
			tpt, ok := t.(addCertHasher)
			if !ok {
				continue
			}
			addrWithCerthash, added := tpt.AddCertHashes(addr)
			if !added {
				log.Warnf("Couldn't add certhashes to multiaddr: %s", addr)
				continue
			}
			addrs[i] = addrWithCerthash
		}
	}
	return addrs
}

func (a *addrsManager) areAddrsDifferent(prev, current []ma.Multiaddr) bool {
	// TODO: make the sorted nature of ma.Unique a guarantee in multiaddrs
	prev = ma.Unique(prev)
	current = ma.Unique(current)
	if len(prev) != len(current) {
		return true
	}
	slices.SortFunc(prev, func(a, b ma.Multiaddr) int { return a.Compare(b) })
	slices.SortFunc(current, func(a, b ma.Multiaddr) int { return a.Compare(b) })
	for i := range prev {
		if !prev[i].Equal(current[i]) {
			return true
		}
	}
	return false
}

const interfaceAddrsCacheTTL = time.Minute

type interfaceAddrsCache struct {
	mx                     sync.RWMutex
	filtered               []ma.Multiaddr
	all                    []ma.Multiaddr
	updateLocalIPv4Backoff backoff.ExpBackoff
	updateLocalIPv6Backoff backoff.ExpBackoff
	lastUpdated            time.Time
}

func (i *interfaceAddrsCache) Filtered() []ma.Multiaddr {
	i.mx.RLock()
	if time.Now().After(i.lastUpdated.Add(interfaceAddrsCacheTTL)) {
		i.mx.RUnlock()
		return i.update(true)
	}
	defer i.mx.RUnlock()
	return i.filtered
}

func (i *interfaceAddrsCache) All() []ma.Multiaddr {
	i.mx.RLock()
	if time.Now().After(i.lastUpdated.Add(interfaceAddrsCacheTTL)) {
		i.mx.RUnlock()
		return i.update(false)
	}
	defer i.mx.RUnlock()
	return i.all
}

func (i *interfaceAddrsCache) update(filtered bool) []ma.Multiaddr {
	i.mx.Lock()
	defer i.mx.Unlock()
	if !time.Now().After(i.lastUpdated.Add(interfaceAddrsCacheTTL)) {
		if filtered {
			return i.filtered
		}
		return i.all
	}
	i.updateUnlocked()
	i.lastUpdated = time.Now()
	if filtered {
		return i.filtered
	}
	return i.all
}

func (i *interfaceAddrsCache) updateUnlocked() {
	i.filtered = nil
	i.all = nil

	// Try to use the default ipv4/6 addresses.
	// TODO: Remove this. We should advertise all interface addresses.
	if r, err := netroute.New(); err != nil {
		log.Debugw("failed to build Router for kernel's routing table", "error", err)
	} else {

		var localIPv4 net.IP
		var ran bool
		err, ran = i.updateLocalIPv4Backoff.Run(func() error {
			_, _, localIPv4, err = r.Route(net.IPv4zero)
			return err
		})

		if ran && err != nil {
			log.Debugw("failed to fetch local IPv4 address", "error", err)
		} else if ran && localIPv4.IsGlobalUnicast() {
			maddr, err := manet.FromIP(localIPv4)
			if err == nil {
				i.filtered = append(i.filtered, maddr)
			}
		}

		var localIPv6 net.IP
		err, ran = i.updateLocalIPv6Backoff.Run(func() error {
			_, _, localIPv6, err = r.Route(net.IPv6unspecified)
			return err
		})

		if ran && err != nil {
			log.Debugw("failed to fetch local IPv6 address", "error", err)
		} else if ran && localIPv6.IsGlobalUnicast() {
			maddr, err := manet.FromIP(localIPv6)
			if err == nil {
				i.filtered = append(i.filtered, maddr)
			}
		}
	}

	// Resolve the interface addresses
	ifaceAddrs, err := manet.InterfaceMultiaddrs()
	if err != nil {
		// This usually shouldn't happen, but we could be in some kind
		// of funky restricted environment.
		log.Errorw("failed to resolve local interface addresses", "error", err)

		// Add the loopback addresses to the filtered addrs and use them as the non-filtered addrs.
		// Then bail. There's nothing else we can do here.
		i.filtered = append(i.filtered, manet.IP4Loopback, manet.IP6Loopback)
		i.all = i.filtered
		return
	}

	// remove link local ipv6 addresses
	i.all = slices.DeleteFunc(ifaceAddrs, manet.IsIP6LinkLocal)

	// If netroute failed to get us any interface addresses, use all of
	// them.
	if len(i.filtered) == 0 {
		// Add all addresses.
		i.filtered = i.all
	} else {
		// Only add loopback addresses. Filter these because we might
		// not _have_ an IPv6 loopback address.
		for _, addr := range i.all {
			if manet.IsIPLoopback(addr) {
				i.filtered = append(i.filtered, addr)
			}
		}
	}
}
