package basichost

import (
	"context"
	"errors"
	"net"
	"slices"
	"sort"
	"sync"

	"github.com/libp2p/go-libp2p/core/network"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// ActivationThresh sets how many times an address must be seen as "activated"
// and therefore advertised to other peers as an address that the local peer
// can be contacted on. The "seen" events expire by default after 40 minutes
// (OwnObservedAddressTTL * ActivationThreshold). The are cleaned up during
// the GC rounds set by GCInterval.
var ActivationThresh = 4

// observedAddrManagerWorkerChannelSize defines how many addresses can be enqueued
// for adding to an ObservedAddrManager.
var observedAddrManagerWorkerChannelSize = 16

const maxExternalThinWaistAddrsPerLocalAddr = 3

// thinWaist is a struct that stores the address along with it's thin waist prefix and rest of the multiaddr
type thinWaist struct {
	Addr, TW, Rest ma.Multiaddr
}

var errTW = errors.New("not a thinwaist address")

func thinWaistForm(a ma.Multiaddr) (thinWaist, error) {
	if len(a) < 2 {
		return thinWaist{}, errTW
	}
	if c0, c1 := a[0].Code(), a[1].Code(); (c0 != ma.P_IP4 && c0 != ma.P_IP6) || (c1 != ma.P_TCP && c1 != ma.P_UDP) {
		return thinWaist{}, errTW
	}
	return thinWaist{Addr: a, TW: a[:2], Rest: a[2:]}, nil
}

// getObserver returns the observer for the multiaddress
// For an IPv4 multiaddress the observer is the IP address
// For an IPv6 multiaddress the observer is the first /56 prefix of the IP address
func getObserver(a ma.Multiaddr) (string, error) {
	ip, err := manet.ToIP(a)
	if err != nil {
		return "", err
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4.String(), nil
	}
	// Count /56 prefix as a single observer.
	return ip.Mask(net.CIDRMask(56, 128)).String(), nil
}

// connMultiaddrs provides IsClosed along with network.ConnMultiaddrs. It is easier to mock this than network.Conn
type connMultiaddrs interface {
	network.ConnMultiaddrs
	IsClosed() bool
}

// observerSetCacheSize is the number of transport sharing the same thinwaist (tcp, ws, wss), (quic, webtransport, webrtc-direct)
// This is 3 in practice right now, but keep a buffer of few extra elements
const observerSetCacheSize = 10

// observerSet is the set of observers who have observed ThinWaistAddr
type observerSet struct {
	ObservedTWAddr ma.Multiaddr
	ObservedBy     map[string]int

	mu               sync.RWMutex            // protects following
	cachedMultiaddrs map[string]ma.Multiaddr // cache of localMultiaddr rest(addr - thinwaist) => output multiaddr
}

func (s *observerSet) cacheMultiaddr(addr ma.Multiaddr) ma.Multiaddr {
	if addr == nil {
		return s.ObservedTWAddr
	}
	addrStr := string(addr.Bytes())
	s.mu.RLock()
	res, ok := s.cachedMultiaddrs[addrStr]
	s.mu.RUnlock()
	if ok {
		return res
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Check if some other go routine added this while we were waiting
	res, ok = s.cachedMultiaddrs[addrStr]
	if ok {
		return res
	}
	if s.cachedMultiaddrs == nil {
		s.cachedMultiaddrs = make(map[string]ma.Multiaddr, observerSetCacheSize)
	}
	if len(s.cachedMultiaddrs) == observerSetCacheSize {
		// remove one entry if we will go over the limit
		for k := range s.cachedMultiaddrs {
			delete(s.cachedMultiaddrs, k)
			break
		}
	}
	s.cachedMultiaddrs[addrStr] = ma.Join(s.ObservedTWAddr, addr)
	return s.cachedMultiaddrs[addrStr]
}

type observation struct {
	conn     connMultiaddrs
	observed ma.Multiaddr
}

// ObservedAddrsManager maps connection's local multiaddrs to their externally observable multiaddress
type ObservedAddrsManager struct {
	// Our listen addrs
	listenAddrs func() []ma.Multiaddr
	// worker channel for new observations
	wch chan observation

	// for closing
	wg        sync.WaitGroup
	ctx       context.Context
	ctxCancel context.CancelFunc

	mu sync.RWMutex
	// local thin waist => external thin waist => observerSet
	externalAddrs map[string]map[string]*observerSet
	// connObservedTWAddrs maps the connection to the last observed thin waist multiaddr on that connection
	connObservedTWAddrs map[connMultiaddrs]ma.Multiaddr
}

// NewObservedAddrManager returns a new address manager using peerstore.OwnObservedAddressTTL as the TTL.
func NewObservedAddrManager(listenAddrs func() []ma.Multiaddr) (*ObservedAddrsManager, error) {
	o := &ObservedAddrsManager{
		externalAddrs:       make(map[string]map[string]*observerSet),
		connObservedTWAddrs: make(map[connMultiaddrs]ma.Multiaddr),
		wch:                 make(chan observation, observedAddrManagerWorkerChannelSize),
		listenAddrs:         listenAddrs,
	}
	o.ctx, o.ctxCancel = context.WithCancel(context.Background())

	return o, nil
}

func (o *ObservedAddrsManager) Start() {
	o.wg.Add(1)
	go o.worker()
}

// AddrsFor return all activated observed addresses associated with the given
// (resolved) listen address.
func (o *ObservedAddrsManager) AddrsFor(addr ma.Multiaddr) (addrs []ma.Multiaddr) {
	if addr == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	tw, err := thinWaistForm(addr)
	if err != nil {
		return nil
	}

	observerSets := o.getTopExternalAddrs(string(tw.TW.Bytes()), ActivationThresh)
	res := make([]ma.Multiaddr, 0, len(observerSets))
	for _, s := range observerSets {
		res = append(res, s.cacheMultiaddr(tw.Rest))
	}
	return res
}

// appendInferredAddrs infers the external address of addresses for the addresses
// that we are listening on using the thin waist mapping.
//
// e.g. If we have observations for a QUIC address on port 9000, and we are
// listening on the same interface and port 9000 for WebTransport, we can infer
// the external WebTransport address.
func (o *ObservedAddrsManager) appendInferredAddrs(twToObserverSets map[string][]*observerSet, addrs []ma.Multiaddr) []ma.Multiaddr {
	if twToObserverSets == nil {
		twToObserverSets = make(map[string][]*observerSet)
		for localTWStr := range o.externalAddrs {
			twToObserverSets[localTWStr] = append(twToObserverSets[localTWStr], o.getTopExternalAddrs(localTWStr, ActivationThresh)...)
		}
	}
	lAddrs := o.listenAddrs()
	seenTWs := make(map[string]struct{})
	for _, a := range lAddrs {
		if _, ok := seenTWs[string(a.Bytes())]; ok {
			// We've already added this
			continue
		}
		seenTWs[string(a.Bytes())] = struct{}{}
		t, err := thinWaistForm(a)
		if err != nil {
			continue
		}
		for _, s := range twToObserverSets[string(t.TW.Bytes())] {
			addrs = append(addrs, s.cacheMultiaddr(t.Rest))
		}
	}
	return addrs
}

// Addrs return all observed addresses with at least minObservers observers
// If minObservers <= 0, it will return all addresses with at least ActivationThresh observers.
func (o *ObservedAddrsManager) Addrs(minObservers int) []ma.Multiaddr {
	o.mu.RLock()
	defer o.mu.RUnlock()

	if minObservers <= 0 {
		minObservers = ActivationThresh
	}

	m := make(map[string][]*observerSet)
	for localTWStr := range o.externalAddrs {
		m[localTWStr] = append(m[localTWStr], o.getTopExternalAddrs(localTWStr, minObservers)...)
	}
	addrs := make([]ma.Multiaddr, 0, maxExternalThinWaistAddrsPerLocalAddr*5) // assume 5 transports
	addrs = o.appendInferredAddrs(m, addrs)
	return addrs
}

func (o *ObservedAddrsManager) getTopExternalAddrs(localTWStr string, minObservers int) []*observerSet {
	observerSets := make([]*observerSet, 0, len(o.externalAddrs[localTWStr]))
	for _, v := range o.externalAddrs[localTWStr] {
		if len(v.ObservedBy) >= minObservers {
			observerSets = append(observerSets, v)
		}
	}
	slices.SortFunc(observerSets, func(a, b *observerSet) int {
		diff := len(b.ObservedBy) - len(a.ObservedBy)
		if diff != 0 {
			return diff
		}
		// In case we have elements with equal counts,
		// keep the address list stable by using the lexicographically smaller address
		return a.ObservedTWAddr.Compare(b.ObservedTWAddr)
	})
	// TODO(sukunrt): Improve this logic. Return only if the addresses have a
	// threshold fraction of the maximum observations
	n := len(observerSets)
	if n > maxExternalThinWaistAddrsPerLocalAddr {
		n = maxExternalThinWaistAddrsPerLocalAddr
	}
	return observerSets[:n]
}

// Record enqueues an observation for recording
func (o *ObservedAddrsManager) Record(conn connMultiaddrs, observed ma.Multiaddr) {
	select {
	case o.wch <- observation{
		conn:     conn,
		observed: observed,
	}:
	default:
		log.Debugw("dropping address observation due to full buffer",
			"from", conn.RemoteMultiaddr(),
			"observed", observed,
		)
	}
}

func (o *ObservedAddrsManager) worker() {
	defer o.wg.Done()
	for {
		select {
		case obs := <-o.wch:
			o.maybeRecordObservation(obs.conn, obs.observed)
		case <-o.ctx.Done():
			return
		}
	}
}

func isRelayedAddress(a ma.Multiaddr) bool {
	for _, c := range a {
		if c.Code() == ma.P_CIRCUIT {
			return true
		}
	}
	return false
}

func (o *ObservedAddrsManager) shouldRecordObservation(conn connMultiaddrs, observed ma.Multiaddr) (shouldRecord bool, localTW thinWaist, observedTW thinWaist) {
	if conn == nil || observed == nil {
		return false, thinWaist{}, thinWaist{}
	}
	// Ignore observations from loopback nodes. We already know our loopback
	// addresses.
	if manet.IsIPLoopback(observed) {
		return false, thinWaist{}, thinWaist{}
	}

	// Provided by NAT64 peers, these addresses are specific to the peer and not publicly routable
	if manet.IsNAT64IPv4ConvertedIPv6Addr(observed) {
		return false, thinWaist{}, thinWaist{}
	}

	// Ignore p2p-circuit addresses. These are the observed address of the relay.
	// Not useful for us.
	if isRelayedAddress(observed) {
		return false, thinWaist{}, thinWaist{}
	}

	localTW, err := thinWaistForm(conn.LocalMultiaddr())
	if err != nil {
		return false, thinWaist{}, thinWaist{}
	}

	listenAddrs := o.listenAddrs()
	for i, a := range listenAddrs {
		tw, err := thinWaistForm(a)
		if err != nil {
			listenAddrs[i] = nil
			continue
		}
		listenAddrs[i] = tw.TW
	}

	if !ma.Contains(listenAddrs, localTW.TW) {
		// not in our list
		return false, thinWaist{}, thinWaist{}
	}

	observedTW, err = thinWaistForm(observed)
	if err != nil {
		return false, thinWaist{}, thinWaist{}
	}
	if !hasConsistentTransport(localTW.TW, observedTW.TW) {
		log.Debugf("invalid observed address %s for local address %s", observed, localTW.Addr)
		return
	}

	return true, localTW, observedTW
}

func (o *ObservedAddrsManager) maybeRecordObservation(conn connMultiaddrs, observed ma.Multiaddr) {
	shouldRecord, localTW, observedTW := o.shouldRecordObservation(conn, observed)
	if !shouldRecord {
		return
	}
	log.Debugw("added own observed listen addr", "conn", conn, "observed", observed)

	o.mu.Lock()
	defer o.mu.Unlock()
	o.recordObservationUnlocked(conn, localTW, observedTW)
}

func (o *ObservedAddrsManager) recordObservationUnlocked(conn connMultiaddrs, localTW, observedTW thinWaist) {
	if conn.IsClosed() {
		// dont record if the connection is already closed. Any previous observations will be removed in
		// the disconnected callback
		return
	}
	localTWStr := string(localTW.TW.Bytes())
	observedTWStr := string(observedTW.TW.Bytes())
	observer, err := getObserver(conn.RemoteMultiaddr())
	if err != nil {
		return
	}

	prevObservedTWAddr, ok := o.connObservedTWAddrs[conn]
	if ok {
		if prevObservedTWAddr.Equal(observedTW.TW) {
			// we have received the same observation again, nothing to do
			return
		} else {
			// if we have a previous entry remove it from externalAddrs
			o.removeExternalAddrsUnlocked(observer, localTWStr, string(prevObservedTWAddr.Bytes()))
		}
	}
	o.connObservedTWAddrs[conn] = observedTW.TW
	o.addExternalAddrsUnlocked(observedTW.TW, observer, localTWStr, observedTWStr)
}

func (o *ObservedAddrsManager) removeExternalAddrsUnlocked(observer, localTWStr, observedTWStr string) {
	s, ok := o.externalAddrs[localTWStr][observedTWStr]
	if !ok {
		return
	}
	s.ObservedBy[observer]--
	if s.ObservedBy[observer] <= 0 {
		delete(s.ObservedBy, observer)
	}
	if len(s.ObservedBy) == 0 {
		delete(o.externalAddrs[localTWStr], observedTWStr)
	}
	if len(o.externalAddrs[localTWStr]) == 0 {
		delete(o.externalAddrs, localTWStr)
	}
}

func (o *ObservedAddrsManager) addExternalAddrsUnlocked(observedTWAddr ma.Multiaddr, observer, localTWStr, observedTWStr string) {
	s, ok := o.externalAddrs[localTWStr][observedTWStr]
	if !ok {
		s = &observerSet{
			ObservedTWAddr: observedTWAddr,
			ObservedBy:     make(map[string]int),
		}
		if _, ok := o.externalAddrs[localTWStr]; !ok {
			o.externalAddrs[localTWStr] = make(map[string]*observerSet)
		}
		o.externalAddrs[localTWStr][observedTWStr] = s
	}
	s.ObservedBy[observer]++
}

func (o *ObservedAddrsManager) RemoveConn(conn connMultiaddrs) {
	if conn == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	observedTWAddr, ok := o.connObservedTWAddrs[conn]
	if !ok {
		return
	}
	delete(o.connObservedTWAddrs, conn)

	localTW, err := thinWaistForm(conn.LocalMultiaddr())
	if err != nil {
		return
	}

	observer, err := getObserver(conn.RemoteMultiaddr())
	if err != nil {
		return
	}

	o.removeExternalAddrsUnlocked(observer, string(localTW.TW.Bytes()), string(observedTWAddr.Bytes()))
}

func (o *ObservedAddrsManager) getNATType() (tcpNATType, udpNATType network.NATDeviceType) {
	o.mu.RLock()
	defer o.mu.RUnlock()

	var tcpCounts, udpCounts []int
	var tcpTotal, udpTotal int
	for _, m := range o.externalAddrs {
		isTCP := false
		for _, v := range m {
			for _, c := range v.ObservedTWAddr {
				if c.Code() == ma.P_TCP {
					isTCP = true
					break
				}
			}
		}
		for _, v := range m {
			if isTCP {
				tcpCounts = append(tcpCounts, len(v.ObservedBy))
				tcpTotal += len(v.ObservedBy)
			} else {
				udpCounts = append(udpCounts, len(v.ObservedBy))
				udpTotal += len(v.ObservedBy)
			}
		}
	}

	sort.Sort(sort.Reverse(sort.IntSlice(tcpCounts)))
	sort.Sort(sort.Reverse(sort.IntSlice(udpCounts)))

	tcpTopCounts, udpTopCounts := 0, 0
	for i := 0; i < maxExternalThinWaistAddrsPerLocalAddr && i < len(tcpCounts); i++ {
		tcpTopCounts += tcpCounts[i]
	}
	for i := 0; i < maxExternalThinWaistAddrsPerLocalAddr && i < len(udpCounts); i++ {
		udpTopCounts += udpCounts[i]
	}

	// If the top elements cover more than 1/2 of all the observations, there's a > 50% chance that
	// hole punching based on outputs of observed address manager will succeed
	//
	// The `3*maxExternalThinWaistAddrsPerLocalAddr` is a magic number, we just want sufficient
	// observations to decide about NAT Type
	if tcpTotal >= 3*maxExternalThinWaistAddrsPerLocalAddr {
		if tcpTopCounts >= tcpTotal/2 {
			tcpNATType = network.NATDeviceTypeCone
		} else {
			tcpNATType = network.NATDeviceTypeSymmetric
		}
	}
	if udpTotal >= 3*maxExternalThinWaistAddrsPerLocalAddr {
		if udpTopCounts >= udpTotal/2 {
			udpNATType = network.NATDeviceTypeCone
		} else {
			udpNATType = network.NATDeviceTypeSymmetric
		}
	}
	return
}

func (o *ObservedAddrsManager) Close() error {
	o.ctxCancel()
	o.wg.Wait()
	return nil
}

// hasConsistentTransport returns true if the thin waist address `aTW` shares the same
// protocols with `bTW`
func hasConsistentTransport(aTW, bTW ma.Multiaddr) bool {
	if len(aTW) != len(bTW) {
		return false
	}
	for i, a := range aTW {
		if bTW[i].Code() != a.Code() {
			return false
		}
	}
	return true
}
