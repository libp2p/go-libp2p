package basichost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/host/basic/internal/backoff"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-netroute"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/prometheus/client_golang/prometheus"
)

const maxObservedAddrsPerListenAddr = 3

// addrChangeTickrInterval is the interval to recompute host addrs.
var addrChangeTickrInterval = 5 * time.Second

// ObservedAddrsManager maps our local listen addrs to externally observed addrs.
type ObservedAddrsManager interface {
	Addrs(minObservers int) []ma.Multiaddr
	AddrsFor(local ma.Multiaddr) []ma.Multiaddr
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
	addCertHashes            func([]ma.Multiaddr) []ma.Multiaddr
	observedAddrsManager     ObservedAddrsManager
	interfaceAddrs           *interfaceAddrsCache
	addrsReachabilityTracker *addrsReachabilityTracker

	// addrsUpdatedChan is notified when addrs change. This is provided by the caller.
	addrsUpdatedChan chan struct{}

	// triggerAddrsUpdateChan is used to trigger an addresses update.
	triggerAddrsUpdateChan chan chan struct{}
	// started is used to check whether the addrsManager has started.
	started atomic.Bool
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
	addCertHashes func([]ma.Multiaddr) []ma.Multiaddr,
	observedAddrsManager ObservedAddrsManager,
	addrsUpdatedChan chan struct{},
	client autonatv2Client,
	enableMetrics bool,
	registerer prometheus.Registerer,
) (*addrsManager, error) {
	ctx, cancel := context.WithCancel(context.Background())
	as := &addrsManager{
		bus:                       bus,
		listenAddrs:               listenAddrs,
		addCertHashes:             addCertHashes,
		observedAddrsManager:      observedAddrsManager,
		natManager:                natmgr,
		addrsFactory:              addrsFactory,
		triggerAddrsUpdateChan:    make(chan chan struct{}, 1),
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
			log.Warn("error closing natmgr", "err", err)
		}
	}
	if a.addrsReachabilityTracker != nil {
		err := a.addrsReachabilityTracker.Close()
		if err != nil {
			log.Warn("error closing addrs reachability tracker", "err", err)
		}
	}
	a.wg.Wait()
}

func (a *addrsManager) NetNotifee() network.Notifiee {
	return &network.NotifyBundle{
		ListenF:      func(network.Network, ma.Multiaddr) { a.updateAddrsSync() },
		ListenCloseF: func(network.Network, ma.Multiaddr) { a.updateAddrsSync() },
	}
}

func (a *addrsManager) updateAddrsSync() {
	// This prevents a deadlock where addrs updates before starting the manager are ignored
	if !a.started.Load() {
		return
	}
	ch := make(chan struct{})
	select {
	case a.triggerAddrsUpdateChan <- ch:
		select {
		case <-ch:
		case <-a.ctx.Done():
		}
	case <-a.ctx.Done():
	}
}

func (a *addrsManager) startBackgroundWorker() (retErr error) {
	autoRelayAddrsSub, err := a.bus.Subscribe(new(event.EvtAutoRelayAddrsUpdated), eventbus.Name("addrs-manager autorelay sub"))
	if err != nil {
		return fmt.Errorf("error subscribing to auto relay addrs: %s", err)
	}
	mc := multiCloser{autoRelayAddrsSub}
	autonatReachabilitySub, err := a.bus.Subscribe(new(event.EvtLocalReachabilityChanged), eventbus.Name("addrs-manager autonatv1 sub"))
	if err != nil {
		return errors.Join(
			fmt.Errorf("error subscribing to autonat reachability: %s", err),
			mc.Close(),
		)
	}
	mc = append(mc, autonatReachabilitySub)

	emitter, err := a.bus.Emitter(new(event.EvtHostReachableAddrsChanged), eventbus.Stateful)
	if err != nil {
		return errors.Join(
			fmt.Errorf("error creating reachability subscriber: %s", err),
			mc.Close(),
		)
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
	// this ensures that listens concurrent with Start are reflected correctly after Start exits.
	a.started.Store(true)
	// update addresses before starting the worker loop. This ensures that any address updates
	// before calling addrsManager.Start are correctly reported after Start returns.
	a.updateAddrs(relayAddrs)

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
			log.Warn("error closing auto relay addrs sub", "err", err)
		}
		err = autonatReachabilitySub.Close()
		if err != nil {
			log.Warn("error closing autonat reachability sub", "err", err)
		}
		err = emitter.Close()
		if err != nil {
			log.Warn("error closing host reachability emitter", "err", err)
		}
	}()

	ticker := time.NewTicker(addrChangeTickrInterval)
	defer ticker.Stop()
	var previousAddrs hostAddrs
	var notifCh chan struct{}
	for {
		currAddrs := a.updateAddrs(relayAddrs)
		if notifCh != nil {
			close(notifCh)
			notifCh = nil
		}
		a.notifyAddrsChanged(emitter, previousAddrs, currAddrs)
		previousAddrs = currAddrs
		select {
		case <-ticker.C:
		case notifCh = <-a.triggerAddrsUpdateChan:
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
// addrs. This must only be called from the background goroutine or from the Start method otherwise
// we may end up with stale addrs.
func (a *addrsManager) updateAddrs(relayAddrs []ma.Multiaddr) hostAddrs {
	localAddrs := a.getLocalAddrs()
	var currReachableAddrs, currUnreachableAddrs, currUnknownAddrs []ma.Multiaddr
	if a.addrsReachabilityTracker != nil {
		currReachableAddrs, currUnreachableAddrs, currUnknownAddrs = a.getConfirmedAddrs(localAddrs)
	}
	relayAddrs = slices.Clone(relayAddrs)
	currAddrs := a.getAddrs(slices.Clone(localAddrs), relayAddrs)

	a.addrsMx.Lock()
	a.currentAddrs = hostAddrs{
		addrs:            append(a.currentAddrs.addrs[:0], currAddrs...),
		localAddrs:       append(a.currentAddrs.localAddrs[:0], localAddrs...),
		reachableAddrs:   append(a.currentAddrs.reachableAddrs[:0], currReachableAddrs...),
		unreachableAddrs: append(a.currentAddrs.unreachableAddrs[:0], currUnreachableAddrs...),
		unknownAddrs:     append(a.currentAddrs.unknownAddrs[:0], currUnknownAddrs...),
		relayAddrs:       append(a.currentAddrs.relayAddrs[:0], relayAddrs...),
	}
	a.addrsMx.Unlock()

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
		log.Debug("host local addresses updated", "addrs", current.localAddrs)
		if a.addrsReachabilityTracker != nil {
			a.addrsReachabilityTracker.UpdateAddrs(current.localAddrs)
		}
	}
	if areAddrsDifferent(previous.addrs, current.addrs) {
		log.Debug("host addresses updated", "addrs", current.localAddrs)
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
		log.Debug("host reachable addrs updated",
			"reachable", current.reachableAddrs,
			"unreachable", current.unreachableAddrs,
			"unknown", current.unknownAddrs)
		if err := emitter.Emit(event.EvtHostReachableAddrsChanged{
			Reachable:   slices.Clone(current.reachableAddrs),
			Unreachable: slices.Clone(current.unreachableAddrs),
			Unknown:     slices.Clone(current.unknownAddrs),
		}); err != nil {
			log.Error("error sending host reachable addrs changed event", "err", err)
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
		// For holepunching, include all the best addresses we know even ones with only 1 observer.
		addrs = append(addrs, a.observedAddrsManager.Addrs(1)...)
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
	if a.natManager != nil {
		finalAddrs = a.appendNATAddrs(finalAddrs, listenAddrs)
	}
	if a.observedAddrsManager != nil {
		finalAddrs = a.appendObservedAddrs(finalAddrs, listenAddrs, a.interfaceAddrs.All())
	}

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
		log.Warn("failed to resolve listen addrs", "err", err)
	} else {
		dst = append(dst, resolved...)
	}
	return dst
}

// appendNATAddrs appends the NAT-ed addrs for the listenAddrs. For unspecified listen addrs it appends the
// public address for all the interfaces.
// Inferring WebTransport from QUIC depends on the observed address manager.
func (a *addrsManager) appendNATAddrs(dst []ma.Multiaddr, listenAddrs []ma.Multiaddr) []ma.Multiaddr {
	for _, listenAddr := range listenAddrs {
		natAddr := a.natManager.GetMapping(listenAddr)
		if natAddr != nil {
			dst = append(dst, natAddr)
		}
	}
	return dst
}

func (a *addrsManager) appendObservedAddrs(dst []ma.Multiaddr, listenAddrs, ifaceAddrs []ma.Multiaddr) []ma.Multiaddr {
	// Add it for all the listenAddr first.
	// listenAddr maybe unspecified. That's okay as connections on UDP transports
	// will have the unspecified address as the local address.
	for _, la := range listenAddrs {
		obsAddrs := a.observedAddrsManager.AddrsFor(la)
		if len(obsAddrs) > maxObservedAddrsPerListenAddr {
			obsAddrs = obsAddrs[:maxObservedAddrsPerListenAddr]
		}
		dst = append(dst, obsAddrs...)
	}

	// if it can be resolved into more addresses, add them too
	resolved, err := manet.ResolveUnspecifiedAddresses(listenAddrs, ifaceAddrs)
	if err != nil {
		log.Warn("failed to resolve listen addr", "listen_addr", listenAddrs, "iface_addrs", ifaceAddrs, "err", err)
		return dst
	}
	for _, addr := range resolved {
		obsAddrs := a.observedAddrsManager.AddrsFor(addr)
		if len(obsAddrs) > maxObservedAddrsPerListenAddr {
			obsAddrs = obsAddrs[:maxObservedAddrsPerListenAddr]
		}
		dst = append(dst, obsAddrs...)
	}
	return dst
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
		log.Debug("failed to build Router for kernel's routing table", "err", err)
	} else {

		var localIPv4 net.IP
		var ran bool
		err, ran = i.updateLocalIPv4Backoff.Run(func() error {
			_, _, localIPv4, err = r.Route(net.IPv4zero)
			return err
		})

		if ran && err != nil {
			log.Debug("failed to fetch local IPv4 address", "err", err)
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
			log.Debug("failed to fetch local IPv6 address", "err", err)
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
		log.Error("failed to resolve local interface addresses", "err", err)

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

type multiCloser []io.Closer

func (mc *multiCloser) Close() error {
	var errs []error
	for _, closer := range *mc {
		if err := closer.Close(); err != nil {
			var closerName string
			if named, ok := closer.(interface{ Name() string }); ok {
				closerName = named.Name()
			} else {
				closerName = fmt.Sprintf("%T", closer)
			}
			errs = append(errs, fmt.Errorf("error closing %s: %w", closerName, err))
		}
	}
	return errors.Join(errs...)
}
