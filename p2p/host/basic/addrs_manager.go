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

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/record"
	"github.com/libp2p/go-libp2p/p2p/host/basic/internal/backoff"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-netroute"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// addrChangeTickrInterval is the interval to recompute host addrs.
	addrChangeTickrInterval = 5 * time.Second
	// natTypeChageTickrInterval is the interval to recompute host nat type.
	natTypeChangeTickrInterval = time.Minute
)

const maxPeerRecordSize = 8 * 1024 // 8k to be compatible with identify's limit

// addrStore is a minimal interface for storing peer addresses
type addrStore interface {
	SetAddrs(peer.ID, []ma.Multiaddr, time.Duration)
}

type observedAddrsManager interface {
	Addrs(minObservers int) []ma.Multiaddr
	AddrsFor(local ma.Multiaddr) []ma.Multiaddr

	Record(conn connMultiaddrs, observed ma.Multiaddr)
	RemoveConn(conn connMultiaddrs)
	Start()
	getNATType() (network.NATDeviceType, network.NATDeviceType)
	io.Closer
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
	observedAddrsManager     observedAddrsManager
	interfaceAddrs           *interfaceAddrsCache
	addrsReachabilityTracker *addrsReachabilityTracker

	// triggerAddrsUpdateChan is used to trigger an addresses update.
	triggerAddrsUpdateChan chan struct{}
	// triggerReachabilityUpdate is notified when reachable addrs are updated.
	triggerReachabilityUpdate chan struct{}

	hostReachability atomic.Pointer[network.Reachability]

	addrsMx      sync.RWMutex
	currentAddrs hostAddrs

	signKey           crypto.PrivKey
	addrStore         addrStore
	signedRecordStore peerstore.CertifiedAddrBook
	hostID            peer.ID

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
	disableObservedAddrs bool,
	observedAddrsManager observedAddrsManager,
	client autonatv2Client,
	enableMetrics bool,
	registerer prometheus.Registerer,
	disableSignedPeerRecord bool,
	signKey crypto.PrivKey,
	addrStore addrStore,
	hostID peer.ID,
) (*addrsManager, error) {
	ctx, cancel := context.WithCancel(context.Background())
	as := &addrsManager{
		bus:                       bus,
		listenAddrs:               listenAddrs,
		addCertHashes:             addCertHashes,
		natManager:                natmgr,
		addrsFactory:              addrsFactory,
		triggerAddrsUpdateChan:    make(chan struct{}, 1),
		triggerReachabilityUpdate: make(chan struct{}, 1),
		interfaceAddrs:            &interfaceAddrsCache{},
		signKey:                   signKey,
		addrStore:                 addrStore,
		hostID:                    hostID,
		ctx:                       ctx,
		ctxCancel:                 cancel,
	}
	unknownReachability := network.ReachabilityUnknown
	as.hostReachability.Store(&unknownReachability)

	if !disableObservedAddrs {
		if observedAddrsManager != nil {
			as.observedAddrsManager = observedAddrsManager
		} else {
			om, err := NewObservedAddrManager(func() []ma.Multiaddr {
				l := as.listenAddrs()
				r, err := manet.ResolveUnspecifiedAddresses(l, as.interfaceAddrs.All())
				if err != nil {
					return l
				}
				return append(l, r...)
			})
			if err != nil {
				return nil, fmt.Errorf("failed to create observed addrs manager: %w", err)
			}
			as.observedAddrsManager = om
		}
	}

	if !disableSignedPeerRecord {
		var ok bool
		as.signedRecordStore, ok = as.addrStore.(peerstore.CertifiedAddrBook)
		if !ok {
			return nil, errors.New("peerstore doesn't implement CertifiedAddrBook interface")
		}
	}

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
	if a.observedAddrsManager != nil {
		a.observedAddrsManager.Start()
		err := a.startObservedAddrsWorker()
		if err != nil {
			a.observedAddrsManager.Close()
			return fmt.Errorf("error starting observed addrs worker: %s", err)
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
	if a.observedAddrsManager != nil {
		err := a.observedAddrsManager.Close()
		if err != nil {
			log.Warnf("error closing observed addrs manager: %s", err)
		}
	}
	a.wg.Wait()
}

func (a *addrsManager) NetNotifee() network.Notifiee {
	return &network.NotifyBundle{
		ListenF:      func(network.Network, ma.Multiaddr) { a.triggerAddrsUpdate() },
		ListenCloseF: func(network.Network, ma.Multiaddr) { a.triggerAddrsUpdate() },
		DisconnectedF: func(_ network.Network, conn network.Conn) {
			if a.observedAddrsManager != nil {
				a.observedAddrsManager.RemoveConn(conn)
			}
		},
	}
}

func (a *addrsManager) triggerAddrsUpdate() {
	// Updating addrs in sync provides the nice property that
	// host.Addrs() just after host.Network().Listen(x) will return x
	a.updateAddrs(false, nil)
	select {
	case a.triggerAddrsUpdateChan <- struct{}{}:
	default:
	}
}

func closeIfError(err error, closer io.Closer, name string) error {
	if err != nil {
		err1 := closer.Close()
		if err1 != nil {
			err1 = fmt.Errorf("error closing %s: %w", name, err1)
		}
		return errors.Join(err, err1)
	}
	return nil
}

func (a *addrsManager) startObservedAddrsWorker() (retErr error) {
	identifySub, err := a.bus.Subscribe(new(event.EvtPeerIdentificationCompleted), eventbus.Name("addrs-manager"))
	if err != nil {
		return fmt.Errorf("error subscribing to autonat reachability: %s", err)
	}
	defer func() { retErr = closeIfError(retErr, identifySub, "identify subscription") }()

	natTypeEmitter, err := a.bus.Emitter(new(event.EvtNATDeviceTypeChanged), eventbus.Stateful)
	if err != nil {
		return fmt.Errorf("error creating nat type emitter: %s", err)
	}

	a.wg.Add(1)
	go a.observedAddrsWorker(identifySub, natTypeEmitter)
	return nil
}

func (a *addrsManager) startBackgroundWorker() (retErr error) {
	autoRelayAddrsSub, err := a.bus.Subscribe(new(event.EvtAutoRelayAddrsUpdated), eventbus.Name("addrs-manager"))
	if err != nil {
		return fmt.Errorf("error subscribing to auto relay addrs: %s", err)
	}
	defer func() { retErr = closeIfError(retErr, autoRelayAddrsSub, "autorelay subscription") }()

	autonatReachabilitySub, err := a.bus.Subscribe(new(event.EvtLocalReachabilityChanged), eventbus.Name("addrs-manager"))
	if err != nil {
		return fmt.Errorf("error subscribing to autonat reachability: %s", err)
	}
	defer func() { retErr = closeIfError(retErr, autonatReachabilitySub, "autonatReachability subscription") }()

	emitter, err := a.bus.Emitter(new(event.EvtHostReachableAddrsChanged), eventbus.Stateful)
	if err != nil {
		return fmt.Errorf("error creating reachability subscriber: %s", err)
	}

	localAddrsEmitter, err := a.bus.Emitter(new(event.EvtLocalAddressesUpdated), eventbus.Stateful)
	if err != nil {
		return fmt.Errorf("error creating local addrs emitter: %s", err)
	}
	defer func() { retErr = closeIfError(retErr, localAddrsEmitter, "local addrs emitter") }()

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
	go a.background(autoRelayAddrsSub, autonatReachabilitySub, emitter, localAddrsEmitter, relayAddrs)
	return nil
}

func (a *addrsManager) background(
	autoRelayAddrsSub,
	autonatReachabilitySub event.Subscription,
	emitter event.Emitter,
	localAddrsEmitter event.Emitter,
	relayAddrs []ma.Multiaddr,
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
		err = emitter.Close()
		if err != nil {
			log.Warnf("error closing host reachability emitter: %s", err)
		}
		err = localAddrsEmitter.Close()
		if err != nil {
			log.Warnf("error closing local addrs emitter: %s", err)
		}
	}()

	ticker := time.NewTicker(addrChangeTickrInterval)
	defer ticker.Stop()
	var previousAddrs hostAddrs
	for {
		currAddrs := a.updateAddrs(true, relayAddrs)
		a.notifyAddrsChanged(emitter, localAddrsEmitter, previousAddrs, currAddrs)
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

func (a *addrsManager) observedAddrsWorker(identifySub event.Subscription, natTypeEmitter event.Emitter) {
	defer a.wg.Done()
	defer func() {
		err := identifySub.Close()
		if err != nil {
			log.Warnf("error closing identify sub: %s", err)
		}
		err = natTypeEmitter.Close()
		if err != nil {
			log.Warnf("error closing nat emitter: %s", err)
		}
	}()
	natTypeTicker := time.NewTicker(natTypeChangeTickrInterval)
	defer natTypeTicker.Stop()
	var udpNATType, tcpNATType network.NATDeviceType
	for {
		select {
		case e := <-identifySub.Out():
			evt := e.(event.EvtPeerIdentificationCompleted)
			a.observedAddrsManager.Record(evt.Conn, evt.ObservedAddr)
		case <-natTypeTicker.C:
			if *a.hostReachability.Load() == network.ReachabilityPrivate {
				newUDPNAT, newTCPNAT := a.observedAddrsManager.getNATType()
				a.notifyNATTypeChanged(natTypeEmitter, newUDPNAT, newTCPNAT, udpNATType, tcpNATType)
				udpNATType, tcpNATType = newUDPNAT, newTCPNAT
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

func (a *addrsManager) notifyAddrsChanged(emitter event.Emitter, localAddrsEmitter event.Emitter, previous, current hostAddrs) {
	if areAddrsDifferent(previous.localAddrs, current.localAddrs) {
		log.Debugf("host local addresses updated: %s", current.localAddrs)
		if a.addrsReachabilityTracker != nil {
			a.addrsReachabilityTracker.UpdateAddrs(current.localAddrs)
		}
	}
	if areAddrsDifferent(previous.addrs, current.addrs) {
		log.Debugf("host addresses updated: %s", current.addrs)

		// Emit EvtLocalAddressesUpdated event and handle peerstore operations
		a.emitAddrChange(localAddrsEmitter, current.addrs, previous.addrs)
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
		dst = append(dst, obsAddrs...)
	}

	// if it can be resolved into more addresses, add them too
	resolved, err := manet.ResolveUnspecifiedAddresses(listenAddrs, ifaceAddrs)
	if err != nil {
		log.Warnf("failed to resolve listen addr %s, %s: %s", listenAddrs, ifaceAddrs, err)
		return dst
	}
	for _, addr := range resolved {
		dst = append(dst, a.observedAddrsManager.AddrsFor(addr)...)
	}
	return dst
}

func (a *addrsManager) notifyNATTypeChanged(emitter event.Emitter, newUDPNAT, newTCPNAT, oldUDPNAT, oldTCPNAT network.NATDeviceType) {
	if newUDPNAT != oldUDPNAT {
		emitter.Emit(event.EvtNATDeviceTypeChanged{
			TransportProtocol: network.NATTransportUDP,
			NatDeviceType:     newUDPNAT,
		})
	}
	if newTCPNAT != oldTCPNAT {
		emitter.Emit(event.EvtNATDeviceTypeChanged{
			TransportProtocol: network.NATTransportTCP,
			NatDeviceType:     newTCPNAT,
		})
	}
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

// diffAddrs diffs prev and current addrs and returns added, maintained, and removed addrs.
// Both prev and current are expected to be sorted using ma.Compare()
func (a *addrsManager) diffAddrs(prev, current []ma.Multiaddr) (added, maintained, removed []ma.Multiaddr) {
	i, j := 0, 0
	for i < len(prev) && j < len(current) {
		cmp := prev[i].Compare(current[j])
		switch {
		case cmp < 0:
			// prev < current
			removed = append(removed, prev[i])
			i++
		case cmp > 0:
			// current < prev
			added = append(added, current[j])
			j++
		default:
			maintained = append(maintained, current[j])
			i++
			j++
		}
	}
	// All remaining current addresses are added
	added = append(added, current[j:]...)

	// All remaining previous addresses are removed
	removed = append(removed, prev[i:]...)
	return
}

// makeSignedPeerRecord creates a signed peer record for the given addresses
func (a *addrsManager) makeSignedPeerRecord(addrs []ma.Multiaddr) (*record.Envelope, error) {
	if a.signKey == nil {
		return nil, fmt.Errorf("signKey is nil")
	}
	// Limit the length of currentAddrs to ensure that our signed peer records aren't rejected
	peerRecordSize := 64 // HostID
	k, err := a.signKey.Raw()
	var nk int
	if err == nil {
		nk = len(k)
	} else {
		nk = 1024 // In case of error, use a large enough value.
	}
	peerRecordSize += 2 * nk // 1 for signature, 1 for public key
	// we want the final address list to be small for keeping the signed peer record in size
	addrs = trimHostAddrList(addrs, maxPeerRecordSize-peerRecordSize-256) // 256 B of buffer
	rec := peer.PeerRecordFromAddrInfo(peer.AddrInfo{
		ID:    a.hostID,
		Addrs: addrs,
	})
	return record.Seal(rec, a.signKey)
}

// trimHostAddrList trims the address list to fit within the maximum size
func trimHostAddrList(addrs []ma.Multiaddr, maxSize int) []ma.Multiaddr {
	totalSize := 0
	for _, a := range addrs {
		totalSize += len(a.Bytes())
	}
	if totalSize <= maxSize {
		return addrs
	}

	score := func(addr ma.Multiaddr) int {
		var res int
		if manet.IsPublicAddr(addr) {
			res |= 1 << 12
		} else if !manet.IsIPLoopback(addr) {
			res |= 1 << 11
		}
		var protocolWeight int
		ma.ForEach(addr, func(c ma.Component) bool {
			switch c.Protocol().Code {
			case ma.P_QUIC_V1:
				protocolWeight = 5
			case ma.P_TCP:
				protocolWeight = 4
			case ma.P_WSS:
				protocolWeight = 3
			case ma.P_WEBTRANSPORT:
				protocolWeight = 2
			case ma.P_WEBRTC_DIRECT:
				protocolWeight = 1
			case ma.P_P2P:
				return false
			}
			return true
		})
		res |= 1 << protocolWeight
		return res
	}

	slices.SortStableFunc(addrs, func(a, b ma.Multiaddr) int {
		return score(b) - score(a) // b-a for reverse order
	})
	totalSize = 0
	for i, a := range addrs {
		totalSize += len(a.Bytes())
		if totalSize > maxSize {
			addrs = addrs[:i]
			break
		}
	}
	return addrs
}

// emitAddrChange emits an EvtLocalAddressesUpdated event and handles peerstore operations
func (a *addrsManager) emitAddrChange(emitter event.Emitter, currentAddrs []ma.Multiaddr, lastAddrs []ma.Multiaddr) {
	added, maintained, removed := a.diffAddrs(lastAddrs, currentAddrs)
	if len(added) == 0 && len(removed) == 0 {
		return
	}

	// update host addresses in the peer store
	a.addrStore.SetAddrs(a.hostID, currentAddrs, peerstore.PermanentAddrTTL)
	a.addrStore.SetAddrs(a.hostID, removed, 0)

	evt := &event.EvtLocalAddressesUpdated{
		Diffs:   true,
		Current: make([]event.UpdatedAddress, 0, len(currentAddrs)),
		Removed: make([]event.UpdatedAddress, 0, len(removed)),
	}

	for _, addr := range maintained {
		evt.Current = append(evt.Current, event.UpdatedAddress{
			Address: addr,
			Action:  event.Maintained,
		})
	}

	for _, addr := range added {
		evt.Current = append(evt.Current, event.UpdatedAddress{
			Address: addr,
			Action:  event.Added,
		})
	}

	for _, addr := range removed {
		evt.Removed = append(evt.Removed, event.UpdatedAddress{
			Address: addr,
			Action:  event.Removed,
		})
	}

	// Our addresses have changed.
	// store the signed peer record in the peer store.
	if a.signedRecordStore != nil {
		// add signed peer record to the event
		sr, err := a.makeSignedPeerRecord(currentAddrs)
		if err != nil {
			log.Errorf("error creating a signed peer record from the set of current addresses, err=%s", err)
			// drop this change without sending the event.
			return
		}
		if _, err := a.signedRecordStore.ConsumePeerRecord(sr, peerstore.PermanentAddrTTL); err != nil {
			log.Errorf("failed to persist signed peer record in peer store, err=%s", err)
			return
		}
		evt.SignedPeerRecord = sr
	}

	// emit addr change event
	if err := emitter.Emit(*evt); err != nil {
		log.Warnf("error emitting event for updated addrs: %s", err)
	}
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
