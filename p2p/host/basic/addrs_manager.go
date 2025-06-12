package basichost

import (
	"context"
	"errors"
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
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	libp2pwebrtc "github.com/libp2p/go-libp2p/p2p/transport/webrtc"
	libp2pwebtransport "github.com/libp2p/go-libp2p/p2p/transport/webtransport"
	"github.com/libp2p/go-netroute"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/prometheus/client_golang/prometheus"
)

const maxObservedAddrsPerListenAddr = 5

type observedAddrsManager interface {
	OwnObservedAddrs() []ma.Multiaddr
	ObservedAddrsFor(local ma.Multiaddr) []ma.Multiaddr
}

type hostAddrs struct {
	addrs            []ma.Multiaddr
	localAddrs       []ma.Multiaddr
	reachableAddrs   []ma.Multiaddr
	unreachableAddrs []ma.Multiaddr
	unknownAddrs     []ma.Multiaddr
	relayAddrs       []ma.Multiaddr
}

type addrsManager struct {
	bus                      event.Bus
	natManager               NATManager
	addrsFactory             AddrsFactory
	listenAddrs              func() []ma.Multiaddr
	transportForListening    func(ma.Multiaddr) transport.Transport
	observedAddrsManager     observedAddrsManager
	interfaceAddrs           *interfaceAddrsCache
	addrsReachabilityTracker *addrsReachabilityTracker

	// addrsUpdatedChan is notified when addrs change. This is provided by the caller.
	addrsUpdatedChan chan struct{}

	// triggerAddrsUpdateChan is used to trigger an addresses update.
	triggerAddrsUpdateChan chan struct{}
	// triggerReachabilityUpdate is notified when reachable addrs are updated.
	triggerReachabilityUpdate chan struct{}

	hostReachability atomic.Pointer[network.Reachability]

	addrsMx      sync.RWMutex
	currentAddrs hostAddrs

	wg        sync.WaitGroup
	ctx       context.Context
	ctxCancel context.CancelFunc
}

func newAddrsManager(
	bus event.Bus,
	natmgr NATManager,
	addrsFactory AddrsFactory,
	listenAddrs func() []ma.Multiaddr,
	transportForListening func(ma.Multiaddr) transport.Transport,
	observedAddrsManager observedAddrsManager,
	addrsUpdatedChan chan struct{},
	client autonatv2Client,
	enableMetrics bool,
	registerer prometheus.Registerer,
) (*addrsManager, error) {
	ctx, cancel := context.WithCancel(context.Background())
	as := &addrsManager{
		bus:                       bus,
		listenAddrs:               listenAddrs,
		transportForListening:     transportForListening,
		observedAddrsManager:      observedAddrsManager,
		natManager:                natmgr,
		addrsFactory:              addrsFactory,
		triggerAddrsUpdateChan:    make(chan struct{}, 1),
		triggerReachabilityUpdate: make(chan struct{}, 1),
		addrsUpdatedChan:          addrsUpdatedChan,
		interfaceAddrs:            &interfaceAddrsCache{},
		ctx:                       ctx,
		ctxCancel:                 cancel,
	}
	unknownReachability := network.ReachabilityUnknown
	as.hostReachability.Store(&unknownReachability)

	if client != nil {
		var metricsTracker MetricsTracker
		if enableMetrics {
			metricsTracker = newMetricsTracker(withRegisterer(registerer))
		}
		as.addrsReachabilityTracker = newAddrsReachabilityTracker(client, as.triggerReachabilityUpdate, nil, metricsTracker)
	}
	return as, nil
}

func (a *addrsManager) Start() error {
	// TODO: add Start method to NATMgr
	if a.addrsReachabilityTracker != nil {
		err := a.addrsReachabilityTracker.Start()
		if err != nil {
			return fmt.Errorf("error starting addrs reachability tracker: %s", err)
		}
	}

	return a.startBackgroundWorker()
}

func (a *addrsManager) Close() {
	a.ctxCancel()
	if a.natManager != nil {
		err := a.natManager.Close()
		if err != nil {
			log.Warnf("error closing natmgr: %s", err)
		}
	}
	if a.addrsReachabilityTracker != nil {
		err := a.addrsReachabilityTracker.Close()
		if err != nil {
			log.Warnf("error closing addrs reachability tracker: %s", err)
		}
	}
	a.wg.Wait()
}

func (a *addrsManager) NetNotifee() network.Notifiee {
	// Updating addrs in sync provides the nice property that
	// host.Addrs() just after host.Network().Listen(x) will return x
	return &network.NotifyBundle{
		ListenF:      func(network.Network, ma.Multiaddr) { a.triggerAddrsUpdate() },
		ListenCloseF: func(network.Network, ma.Multiaddr) { a.triggerAddrsUpdate() },
	}
}

func (a *addrsManager) triggerAddrsUpdate() {
	a.updateAddrs(false, nil)
	select {
	case a.triggerAddrsUpdateChan <- struct{}{}:
	default:
	}
}

func (a *addrsManager) startBackgroundWorker() error {
	autoRelayAddrsSub, err := a.bus.Subscribe(new(event.EvtAutoRelayAddrsUpdated), eventbus.Name("addrs-manager"))
	if err != nil {
		return fmt.Errorf("error subscribing to auto relay addrs: %s", err)
	}

	autonatReachabilitySub, err := a.bus.Subscribe(new(event.EvtLocalReachabilityChanged), eventbus.Name("addrs-manager"))
	if err != nil {
		err1 := autoRelayAddrsSub.Close()
		if err1 != nil {
			err1 = fmt.Errorf("error closign autorelaysub: %w", err1)
		}
		err = fmt.Errorf("error subscribing to autonat reachability: %s", err)
		return errors.Join(err, err1)
	}

	emitter, err := a.bus.Emitter(new(event.EvtHostReachableAddrsChanged), eventbus.Stateful)
	if err != nil {
		err1 := autoRelayAddrsSub.Close()
		if err1 != nil {
			err1 = fmt.Errorf("error closing autorelaysub: %w", err1)
		}
		err2 := autonatReachabilitySub.Close()
		if err2 != nil {
			err2 = fmt.Errorf("error closing autonat reachability: %w", err1)
		}
		err = fmt.Errorf("error subscribing to autonat reachability: %s", err)
		return errors.Join(err, err1, err2)
	}

	var relayAddrs []ma.Multiaddr
	// update relay addrs in case we're private
	select {
	case e := <-autoRelayAddrsSub.Out():
		if evt, ok := e.(event.EvtAutoRelayAddrsUpdated); ok {
			relayAddrs = slices.Clone(evt.RelayAddrs)
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
	// update addresses before starting the worker loop. This ensures that any address updates
	// before calling addrsManager.Start are correctly reported after Start returns.
	a.updateAddrs(true, relayAddrs)

	a.wg.Add(1)
	go a.background(autoRelayAddrsSub, autonatReachabilitySub, emitter, relayAddrs)
	return nil
}

func (a *addrsManager) background(autoRelayAddrsSub, autonatReachabilitySub event.Subscription,
	emitter event.Emitter, relayAddrs []ma.Multiaddr,
) {
	defer a.wg.Done()
	defer func() {
		err := autoRelayAddrsSub.Close()
		if err != nil {
			log.Warnf("error closing auto relay addrs sub: %s", err)
		}
		err = autonatReachabilitySub.Close()
		if err != nil {
			log.Warnf("error closing autonat reachability sub: %s", err)
		}
	}()

	ticker := time.NewTicker(addrChangeTickrInterval)
	defer ticker.Stop()
	var previousAddrs hostAddrs
	for {
		currAddrs := a.updateAddrs(true, relayAddrs)
		a.notifyAddrsChanged(emitter, previousAddrs, currAddrs)
		previousAddrs = currAddrs
		select {
		case <-ticker.C:
		case <-a.triggerAddrsUpdateChan:
		case <-a.triggerReachabilityUpdate:
		case e := <-autoRelayAddrsSub.Out():
			if evt, ok := e.(event.EvtAutoRelayAddrsUpdated); ok {
				relayAddrs = slices.Clone(evt.RelayAddrs)
			}
		case e := <-autonatReachabilitySub.Out():
			if evt, ok := e.(event.EvtLocalReachabilityChanged); ok {
				a.hostReachability.Store(&evt.Reachability)
			}
		case <-a.ctx.Done():
			return
		}
	}
}

// updateAddrs updates the addresses of the host and returns the new updated
// addrs
func (a *addrsManager) updateAddrs(updateRelayAddrs bool, relayAddrs []ma.Multiaddr) hostAddrs {
	// Must lock while doing both recompute and update as this method is called from
	// multiple goroutines.
	a.addrsMx.Lock()
	defer a.addrsMx.Unlock()

	localAddrs := a.getLocalAddrs()
	var currReachableAddrs, currUnreachableAddrs, currUnknownAddrs []ma.Multiaddr
	if a.addrsReachabilityTracker != nil {
		currReachableAddrs, currUnreachableAddrs, currUnknownAddrs = a.getConfirmedAddrs(localAddrs)
	}
	if !updateRelayAddrs {
		relayAddrs = a.currentAddrs.relayAddrs
	} else {
		// Copy the callers slice
		relayAddrs = slices.Clone(relayAddrs)
	}
	currAddrs := a.getAddrs(slices.Clone(localAddrs), relayAddrs)

	a.currentAddrs = hostAddrs{
		addrs:            append(a.currentAddrs.addrs[:0], currAddrs...),
		localAddrs:       append(a.currentAddrs.localAddrs[:0], localAddrs...),
		reachableAddrs:   append(a.currentAddrs.reachableAddrs[:0], currReachableAddrs...),
		unreachableAddrs: append(a.currentAddrs.unreachableAddrs[:0], currUnreachableAddrs...),
		unknownAddrs:     append(a.currentAddrs.unknownAddrs[:0], currUnknownAddrs...),
		relayAddrs:       append(a.currentAddrs.relayAddrs[:0], relayAddrs...),
	}

	return hostAddrs{
		localAddrs:       localAddrs,
		addrs:            currAddrs,
		reachableAddrs:   currReachableAddrs,
		unreachableAddrs: currUnreachableAddrs,
		unknownAddrs:     currUnknownAddrs,
		relayAddrs:       relayAddrs,
	}
}

func (a *addrsManager) notifyAddrsChanged(emitter event.Emitter, previous, current hostAddrs) {
	if areAddrsDifferent(previous.localAddrs, current.localAddrs) {
		log.Debugf("host local addresses updated: %s", current.localAddrs)
		if a.addrsReachabilityTracker != nil {
			a.addrsReachabilityTracker.UpdateAddrs(current.localAddrs)
		}
	}
	if areAddrsDifferent(previous.addrs, current.addrs) {
		log.Debugf("host addresses updated: %s", current.localAddrs)
		select {
		case a.addrsUpdatedChan <- struct{}{}:
		default:
		}
	}

	// We *must* send both reachability changed and addrs changed events from the
	// same goroutine to ensure correct ordering
	// Consider the events:
	// 	- addr x discovered
	// 	- addr x is reachable
	// 	- addr x removed
	// We must send these events in the same order. It'll be confusing for consumers
	// if the reachable event is received after the addr removed event.
	if areAddrsDifferent(previous.reachableAddrs, current.reachableAddrs) ||
		areAddrsDifferent(previous.unreachableAddrs, current.unreachableAddrs) ||
		areAddrsDifferent(previous.unknownAddrs, current.unknownAddrs) {
		log.Debugf("host reachable addrs updated: %s", current.localAddrs)
		if err := emitter.Emit(event.EvtHostReachableAddrsChanged{
			Reachable:   slices.Clone(current.reachableAddrs),
			Unreachable: slices.Clone(current.unreachableAddrs),
			Unknown:     slices.Clone(current.unknownAddrs),
		}); err != nil {
			log.Errorf("error sending host reachable addrs changed event: %s", err)
		}
	}
}

// Addrs returns the node's dialable addresses both public and private.
// If autorelay is enabled and node reachability is private, it returns
// the node's relay addresses and private network addresses.
func (a *addrsManager) Addrs() []ma.Multiaddr {
	a.addrsMx.RLock()
	directAddrs := slices.Clone(a.currentAddrs.localAddrs)
	relayAddrs := slices.Clone(a.currentAddrs.relayAddrs)
	a.addrsMx.RUnlock()
	return a.getAddrs(directAddrs, relayAddrs)
}

// getAddrs returns the node's dialable addresses. Mutates localAddrs
func (a *addrsManager) getAddrs(localAddrs []ma.Multiaddr, relayAddrs []ma.Multiaddr) []ma.Multiaddr {
	addrs := localAddrs
	rch := a.hostReachability.Load()
	if rch != nil && *rch == network.ReachabilityPrivate {
		// Delete public addresses if the node's reachability is private, and we have relay addresses
		if len(relayAddrs) > 0 {
			addrs = slices.DeleteFunc(addrs, manet.IsPublicAddr)
			addrs = append(addrs, relayAddrs...)
		}
	}
	// Make a copy. Consumers can modify the slice elements
	addrs = slices.Clone(a.addrsFactory(addrs))
	// Add certhashes for the addresses provided by the user via address factory.
	addrs = a.addCertHashes(ma.Unique(addrs))
	slices.SortFunc(addrs, func(a, b ma.Multiaddr) int { return a.Compare(b) })
	return addrs
}

// HolePunchAddrs returns all the host's direct public addresses, reachable or unreachable,
// suitable for hole punching.
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
	return slices.Clone(a.currentAddrs.localAddrs)
}

// ConfirmedAddrs returns all addresses of the host that are reachable from the internet
func (a *addrsManager) ConfirmedAddrs() (reachable []ma.Multiaddr, unreachable []ma.Multiaddr, unknown []ma.Multiaddr) {
	a.addrsMx.RLock()
	defer a.addrsMx.RUnlock()
	return slices.Clone(a.currentAddrs.reachableAddrs), slices.Clone(a.currentAddrs.unreachableAddrs), slices.Clone(a.currentAddrs.unknownAddrs)
}

func (a *addrsManager) getConfirmedAddrs(localAddrs []ma.Multiaddr) (reachableAddrs, unreachableAddrs, unknownAddrs []ma.Multiaddr) {
	reachableAddrs, unreachableAddrs, unknownAddrs = a.addrsReachabilityTracker.ConfirmedAddrs()
	return removeNotInSource(reachableAddrs, localAddrs), removeNotInSource(unreachableAddrs, localAddrs), removeNotInSource(unknownAddrs, localAddrs)
}

var p2pCircuitAddr = ma.StringCast("/p2p-circuit")

func (a *addrsManager) getLocalAddrs() []ma.Multiaddr {
	listenAddrs := a.listenAddrs()
	if len(listenAddrs) == 0 {
		return nil
	}

	finalAddrs := make([]ma.Multiaddr, 0, 8)
	finalAddrs = a.appendPrimaryInterfaceAddrs(finalAddrs, listenAddrs)
	finalAddrs = a.appendNATAddrs(finalAddrs, listenAddrs, a.interfaceAddrs.All())

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
	finalAddrs = ma.Unique(finalAddrs)
	slices.SortFunc(finalAddrs, func(a, b ma.Multiaddr) int { return a.Compare(b) })
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

func areAddrsDifferent(prev, current []ma.Multiaddr) bool {
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

// removeNotInSource removes items from addrs that are not present in source.
// Modifies the addrs slice in place
// addrs and source must be sorted using multiaddr.Compare.
func removeNotInSource(addrs, source []ma.Multiaddr) []ma.Multiaddr {
	j := 0
	// mark entries not in source as nil
	for i, a := range addrs {
		// move right as long as a > source[j]
		for j < len(source) && a.Compare(source[j]) > 0 {
			j++
		}
		// a is not in source if we've reached the end, or a is lesser
		if j == len(source) || a.Compare(source[j]) < 0 {
			addrs[i] = nil
		}
		// a is in source, nothing to do
	}
	// j is the current element, i is the lowest index nil element
	i := 0
	for j := range len(addrs) {
		if addrs[j] != nil {
			addrs[i], addrs[j] = addrs[j], addrs[i]
			i++
		}
	}
	return addrs[:i]
}
