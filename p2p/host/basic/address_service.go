package basichost

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"slices"
	"sync"
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

type observedAddrsService interface {
	OwnObservedAddrs() []ma.Multiaddr
	ObservedAddrsFor(local ma.Multiaddr) []ma.Multiaddr
}

type addressService struct {
	eventbus              event.Bus
	natManager            NATManager
	addrsFactory          AddrsFactory
	addrsChangeChan       chan struct{}
	addrsUpdated          chan struct{}
	listenAddrs           func() []ma.Multiaddr
	transportForListening func(ma.Multiaddr) transport.Transport
	reachability          func() network.Reachability
	observedAddrsService  observedAddrsService

	interfaceAddrs *interfaceAddrsCache
	wg             sync.WaitGroup
	ctx            context.Context
	ctxCancel      context.CancelFunc

	addrsMx    sync.RWMutex
	localAddrs []ma.Multiaddr
	relayAddrs []ma.Multiaddr
}

func NewAddressService(
	eventbus event.Bus,
	natmgr NATManager,
	addrFactory AddrsFactory,
	listenAddrs func() []ma.Multiaddr,
	transportForListening func(ma.Multiaddr) transport.Transport,
	observedAddrsService observedAddrsService,
	reachability func() network.Reachability,
) (*addressService, error) {
	ctx, cancel := context.WithCancel(context.Background())
	as := &addressService{
		eventbus:              eventbus,
		listenAddrs:           listenAddrs,
		transportForListening: transportForListening,
		observedAddrsService:  observedAddrsService,
		natManager:            natmgr,
		addrsFactory:          addrFactory,
		addrsChangeChan:       make(chan struct{}, 1),
		addrsUpdated:          make(chan struct{}, 1),
		interfaceAddrs:        &interfaceAddrsCache{},
		reachability:          reachability,
		ctx:                   ctx,
		ctxCancel:             cancel,
	}
	return as, nil
}

func (a *addressService) Start() error {
	err := a.background()
	if err != nil {
		return fmt.Errorf("error starting address service: %s", err)
	}
	return nil
}

func (a *addressService) Close() {
	a.ctxCancel()
	if a.natManager != nil {
		err := a.natManager.Close()
		if err != nil {
			log.Warnf("error closing natmgr: %s", err)
		}
	}
	a.wg.Wait()
}

func (a *addressService) SignalAddressChange() {
	// This is ugly, we update here and in the background loop, but this ensures the nice property
	// that host.Addrs after host.Network().Listen will return the recently added listen address
	a.updateLocalAddrs()
	select {
	case a.addrsChangeChan <- struct{}{}:
	default:
	}
}

func (a *addressService) AddrsUpdated() chan struct{} {
	return a.addrsUpdated
}

func (a *addressService) background() error {
	autoRelayAddrsSub, err := a.eventbus.Subscribe(new(event.EvtAutoRelayAddrs))
	if err != nil {
		return fmt.Errorf("error subscribing to auto relay addrs: %s", err)
	}

	// ensure that we have the correct address after returning from Start()
	a.updateLocalAddrs()
	select {
	case e := <-autoRelayAddrsSub.Out():
		if evt, ok := e.(*event.EvtAutoRelayAddrs); ok {
			a.updateRelayAddrs(evt.RelayAddrs)
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
		ticker := time.NewTicker(addrChangeTickrInterval)
		defer ticker.Stop()
		var prev []ma.Multiaddr
		for {
			a.updateLocalAddrs()
			curr := a.Addrs()
			if a.areAddrsDifferent(prev, curr) {
				select {
				case a.addrsUpdated <- struct{}{}:
				default:
				}
			}
			prev = curr
			select {
			case <-ticker.C:
			case <-a.addrsChangeChan:
			case e := <-autoRelayAddrsSub.Out():
				if evt, ok := e.(event.EvtAutoRelayAddrs); ok {
					a.updateRelayAddrs(evt.RelayAddrs)
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
func (a *addressService) Addrs() []ma.Multiaddr {
	addrs := a.AllAddrs()
	if a.reachability() == network.ReachabilityPrivate {
		a.addrsMx.RLock()
		// Delete public addresses if the node's reachability is private, and we have relay addresses
		if len(a.relayAddrs) > 0 {
			addrs = slices.DeleteFunc(addrs, func(a ma.Multiaddr) bool { return manet.IsPublicAddr(a) })
			addrs = append(addrs, a.relayAddrs...)
		}
		a.addrsMx.RUnlock()
	}
	// Make a copy. Consumers can modify the slice elements
	addrs = slices.Clone(a.addrsFactory(addrs))
	// Add certhashes for the addresses provided by the user via address factory.
	return a.addCertHashes(ma.Unique(addrs))
}

// GetDirectAddrs returns the node's public direct listen addresses.
func (a *addressService) GetDirectAddrs() []ma.Multiaddr {
	addrs := a.AllAddrs()
	addrs = slices.Clone(a.addrsFactory(addrs))
	// AllAddrs may ignore observed addresses in favour of NAT mappings.
	// Use both for hole punching.
	addrs = append(addrs, a.observedAddrsService.OwnObservedAddrs()...)
	addrs = ma.Unique(addrs)
	return slices.DeleteFunc(addrs, func(a ma.Multiaddr) bool { return !manet.IsPublicAddr(a) })
}

// AllAddrs returns all the addresses the host is listening on except circuit addresses.
func (a *addressService) AllAddrs() []ma.Multiaddr {
	a.addrsMx.RLock()
	defer a.addrsMx.RUnlock()
	return slices.Clone(a.localAddrs)
}

var p2pCircuitAddr = ma.StringCast("/p2p-circuit")

func (a *addressService) updateLocalAddrs() {
	a.addrsMx.Lock()
	defer a.addrsMx.Unlock()
	a.localAddrs = a.getLocalAddrs()
}

func (a *addressService) getLocalAddrs() []ma.Multiaddr {
	listenAddrs := a.listenAddrs()
	if len(listenAddrs) == 0 {
		return nil
	}

	finalAddrs := make([]ma.Multiaddr, 0, 8)
	finalAddrs = a.appendInterfaceAddrs(finalAddrs, listenAddrs)
	finalAddrs = a.appendNATAddrs(finalAddrs, listenAddrs)
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

func (a *addressService) updateRelayAddrs(addrs []ma.Multiaddr) {
	a.addrsMx.Lock()
	defer a.addrsMx.Unlock()
	a.relayAddrs = slices.Clone(addrs)
}

func (a *addressService) appendInterfaceAddrs(result []ma.Multiaddr, listenAddrs []ma.Multiaddr) []ma.Multiaddr {
	// resolving any unspecified listen addressees to use only the primary
	// interface to avoid advertising too many addresses.
	if resolved, err := manet.ResolveUnspecifiedAddresses(listenAddrs, a.interfaceAddrs.Filtered()); err != nil {
		log.Warnw("failed to resolve listen addrs", "error", err)
	} else {
		result = append(result, resolved...)
	}
	result = ma.Unique(result)
	return result
}

// appendNATAddrs appends the NAT-ed addrs for the listenAddrs. For unspecified listen addrs it appends the
// public address for all the interfaces.
// This automatically infers addresses from other transport addresses. For example, it'll infer a webtransport
// address from a quic observed address.
//
// TODO: Merge the natmgr and identify.ObservedAddrManager in to one NatMapper module.
func (a *addressService) appendNATAddrs(result []ma.Multiaddr, listenAddrs []ma.Multiaddr) []ma.Multiaddr {
	ifaceAddrs := a.interfaceAddrs.All()
	if a.natManager == nil || !a.natManager.HasDiscoveredNAT() {
		if a.observedAddrsService != nil {
			result = append(result, a.observedAddrsService.OwnObservedAddrs()...)
		}
		return result
	}
	for _, listen := range listenAddrs {
		extMaddr := a.natManager.GetMapping(listen)
		result = appendNATAddrsForListenAddrs(result, listen, extMaddr, a.observedAddrsService.ObservedAddrsFor, ifaceAddrs)
	}
	return result
}

func (a *addressService) addCertHashes(addrs []ma.Multiaddr) []ma.Multiaddr {
	// TODO(sukunrt): Move this to swarm.
	// swarm should expose a AddCertHashes(addr) method
	// and then it should call AddCertHashes with the relevant transport.
	type transportForListeninger interface {
		TransportForListening(a ma.Multiaddr) transport.Transport
	}

	type addCertHasher interface {
		AddCertHashes(m ma.Multiaddr) (ma.Multiaddr, bool)
	}

	if a.transportForListening == nil {
		return addrs
	}

	for i, addr := range addrs {
		wtOK, wtN := libp2pwebtransport.IsWebtransportMultiaddr(addr)
		webrtcOK, webrtcN := libp2pwebrtc.IsWebRTCDirectMultiaddr(addr)
		if (wtOK && wtN == 0) || (webrtcOK && webrtcN == 0) {
			t := a.transportForListening(addr)
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

func (a *addressService) areAddrsDifferent(prev, current []ma.Multiaddr) bool {
	prev = ma.Unique(prev)
	current = ma.Unique(current)
	slices.SortFunc(prev, func(a, b ma.Multiaddr) int { return bytes.Compare(a.Bytes(), b.Bytes()) })
	slices.SortFunc(current, func(a, b ma.Multiaddr) int { return bytes.Compare(a.Bytes(), b.Bytes()) })
	if len(prev) != len(current) {
		return true
	}
	for i := range prev {
		if !prev[i].Equal(current[i]) {
			return true
		}
	}
	return false
}

// getAllPossibleLocalAddrs gives all the possible address returned for `conn.LocalAddr` corresponding
// to the `listenAddr`
func getAllPossibleLocalAddrs(listenAddr ma.Multiaddr, ifaceAddrs []ma.Multiaddr) []ma.Multiaddr {
	// If the nat mapping fails, use the observed addrs
	resolved, err := manet.ResolveUnspecifiedAddress(listenAddr, ifaceAddrs)
	if err != nil {
		log.Warnf("failed to resolve listen addr %s, %s: %s", listenAddr, ifaceAddrs, err)
		return nil
	}
	return append(resolved, listenAddr)
}

// appendNATAddrsForListenAddrs adds the NAT-ed addresses to the result. If the NAT device doesn't provide
// us with a public IP address, we use the observed addresses.
func appendNATAddrsForListenAddrs(result []ma.Multiaddr, listenAddr ma.Multiaddr, natMapping ma.Multiaddr,
	obsAddrsFunc func(ma.Multiaddr) []ma.Multiaddr,
	ifaceAddrs []ma.Multiaddr) []ma.Multiaddr {
	if natMapping == nil {
		allAddrs := getAllPossibleLocalAddrs(listenAddr, ifaceAddrs)
		for _, a := range allAddrs {
			result = append(result, obsAddrsFunc(a)...)
		}
		return result
	}

	// if the router reported a sane address, use it.
	if !manet.IsIPUnspecified(natMapping) {
		result = append(result, natMapping)
	} else {
		log.Warn("NAT device reported an unspecified IP as it's external address")
	}

	// If the router gave us a public address, use it and ignore observed addresses
	if manet.IsPublicAddr(natMapping) {
		return result
	}

	// Router gave us a private IP; maybe we're behind a CGNAT.
	// See if we have a public IP from observed addresses.
	_, extMaddrNoIP := ma.SplitFirst(natMapping)
	if extMaddrNoIP == nil {
		return result
	}

	allAddrs := getAllPossibleLocalAddrs(listenAddr, ifaceAddrs)
	for _, addr := range allAddrs {
		for _, obsMaddr := range obsAddrsFunc(addr) {
			// Extract a public observed addr.
			ip, _ := ma.SplitFirst(obsMaddr)
			if ip == nil || !manet.IsPublicAddr(ip) {
				continue
			}
			result = append(result, ma.Join(ip, extMaddrNoIP))
		}
	}
	return result
}

const ifaceAddrsTTL = time.Minute

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
	if time.Now().After(i.lastUpdated.Add(ifaceAddrsTTL)) {
		i.mx.RUnlock()
		return i.update(true)
	}
	defer i.mx.RUnlock()
	return i.filtered
}

func (i *interfaceAddrsCache) All() []ma.Multiaddr {
	i.mx.RLock()
	if time.Now().After(i.lastUpdated.Add(ifaceAddrsTTL)) {
		i.mx.RUnlock()
		return i.update(false)
	}
	defer i.mx.RUnlock()
	return i.all
}

func (i *interfaceAddrsCache) update(filtered bool) []ma.Multiaddr {
	i.mx.Lock()
	defer i.mx.Unlock()
	if !time.Now().After(i.lastUpdated.Add(ifaceAddrsTTL)) {
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
