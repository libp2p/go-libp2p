package basichost

import (
	"context"
	"strconv"
	"sync"
	"time"

	goprocess "github.com/jbenet/goprocess"
	lgbl "github.com/libp2p/go-libp2p-loggables"
	inat "github.com/libp2p/go-libp2p-nat"
	inet "github.com/libp2p/go-libp2p-net"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"
	"github.com/multiformats/go-multiaddr"
	ma "github.com/multiformats/go-multiaddr"
)

// A simple interface to manage NAT devices.
type NATManager interface {

	// Get the NAT device managed by the NAT manager.
	NAT() *inat.NAT

	// Receive a notification when the NAT device is ready for use.
	Ready() <-chan struct{}

	// Close all resources associated with a NAT manager.
	Close() error
}

// Create a NAT manager.
func NewNATManager(net inet.Network, ids *identify.IDService) NATManager {
	return newNatManager(net, ids)
}

// natManager takes care of adding + removing port mappings to the nat.
// Initialized with the host if it has a NATPortMap option enabled.
// natManager receives signals from the network, and check on nat mappings:
//  * natManager listens to the network and adds or closes port mappings
//    as the network signals Listen() or ListenClose().
//  * closing the natManager closes the nat and its mappings.
type natManager struct {
	net       inet.Network
	idService *identify.IDService
	natmu     sync.RWMutex // guards nat (ready could obviate this mutex, but safety first.)
	nat       *inat.NAT

	ready chan struct{}     // closed once the nat is ready to process port mappings
	proc  goprocess.Process // natManager has a process + children. can be closed.
}

func newNatManager(net inet.Network, ids *identify.IDService) *natManager {
	nmgr := &natManager{
		net:       net,
		idService: ids,
		ready:     make(chan struct{}),
	}

	nmgr.proc = goprocess.WithTeardown(func() error {
		// on closing, unregister from network notifications.
		net.StopNotify((*nmgrNetNotifiee)(nmgr))
		return nil
	})

	// discover the nat.
	nmgr.discoverNAT()
	// sync observed addresses with NAT port mapping configs
	nmgr.syncObservedAddresses()
	return nmgr
}

// Close closes the natManager, closing the underlying nat
// and unregistering from network events.
func (nmgr *natManager) Close() error {
	return nmgr.proc.Close()
}

// Ready returns a channel which will be closed when the NAT has been found
// and is ready to be used, or the search process is done.
func (nmgr *natManager) Ready() <-chan struct{} {
	return nmgr.ready
}

func (nmgr *natManager) discoverNAT() {

	nmgr.proc.Go(func(worker goprocess.Process) {
		// inat.DiscoverNAT blocks until the nat is found or a timeout
		// is reached. we unfortunately cannot specify timeouts-- the
		// library we're using just blocks.
		//
		// Note: on early shutdown, there may be a case where we're trying
		// to close before DiscoverNAT() returns. Since we cant cancel it
		// (library) we can choose to (1) drop the result and return early,
		// or (2) wait until it times out to exit. For now we choose (2),
		// to avoid leaking resources in a non-obvious way. the only case
		// this affects is when the daemon is being started up and _immediately_
		// asked to close. other services are also starting up, so ok to wait.
		discoverdone := make(chan struct{})
		var nat *inat.NAT
		go func() {
			defer close(discoverdone)
			nat = inat.DiscoverNAT()
		}()

		// by this point -- after finding the NAT -- we may have already
		// be closing. if so, just exit.
		select {
		case <-worker.Closing():
			return
		case <-discoverdone:
			if nat == nil { // no nat, or failed to get it.
				return
			}
		}

		// wire up the nat to close when nmgr closes.
		// nmgr.proc is our parent, and waiting for us.
		nmgr.proc.AddChild(nat.Process())

		// set the nat.
		nmgr.natmu.Lock()
		nmgr.nat = nat
		nmgr.natmu.Unlock()

		// signal that we're ready to process nat mappings:
		close(nmgr.ready)

		// sign natManager up for network notifications
		// we need to sign up here to avoid missing some notifs
		// before the NAT has been found.
		nmgr.net.Notify((*nmgrNetNotifiee)(nmgr))

		// if any interfaces were brought up while we were setting up
		// the nat, now is the time to setup port mappings for them.
		// we release ready, then grab them to avoid losing any. adding
		// a port mapping is idempotent, so its ok to add the same twice.
		addrs := nmgr.net.ListenAddresses()
		for _, addr := range addrs {
			// we do it async because it's slow and we may want to close beforehand
			go addPortMapping(nmgr, addr, inat.RandomMappingExternalPort)
		}
	})
}

// parseAddress extracts IP address and port from a Multiaddr
func parseAddress(a ma.Multiaddr) (ip string, port int, err error) {
	ip, err = a.ValueForProtocol(multiaddr.P_IP4)
	if err != nil {
		ip, err = a.ValueForProtocol(multiaddr.P_IP6)
		if err != nil { // don't handle circuit-relay and other addresses
			return
		}
	}
	portString, err := a.ValueForProtocol(multiaddr.P_TCP)
	if err != nil {
		return
	}
	port, err = strconv.Atoi(portString)
	return ip, port, err
}

func parseLocalIPs(listenAddresses []ma.Multiaddr) (localIPs []string) {
	for _, la := range listenAddresses {
		ip, _, err := parseAddress(la)
		if err == nil {
			localIPs = append(localIPs, ip)
		}
	}
	return localIPs
}

func parseExtraPorts(observed []ma.Multiaddr, mapped []inat.Mapping,
	localIPs []string) (extraPorts []int) {

	alreadyMapped := make(map[string]bool)
	for _, m := range mapped {
		externalAddr, err := m.ExternalAddr()
		if err != nil {
			continue
		}
		alreadyMapped[externalAddr.String()] = true
	}

	isLocal := make(map[string]bool)
	for _, ip := range localIPs {
		isLocal[ip] = true
	}

	for _, a := range observed {
		if alreadyMapped[a.String()] {
			continue
		}
		ip, port, err := parseAddress(a)
		if err != nil {
			continue
		}
		if isLocal[ip] {
			continue
		}
		extraPorts = append(extraPorts, port)
	}

	return extraPorts
}

func parseExcessivePorts(observed []ma.Multiaddr, mapped []inat.Mapping) (excessiveMaps []inat.Mapping) {
	observedMap := make(map[string]bool)
	for _, a := range observed {
		observedMap[a.String()] = true
	}
	for _, m := range mapped {
		externalAddr, err := m.ExternalAddr()
		if err != nil {
			continue
		}
		if !observedMap[externalAddr.String()] {
			excessiveMaps = append(excessiveMaps, m)
		}
	}
	return excessiveMaps
}

const closeMappingThreshold = 10

func (nmgr *natManager) syncObservedAddresses() {
	nmgr.proc.Go(func(worker goprocess.Process) {
		// wait until NAT is ready, or natManager quits
		select {
		case <-worker.Closing():
			return
		case <-nmgr.ready:
		}

		nmgr.natmu.Lock()
		if nmgr.nat == nil {
			nmgr.natmu.Unlock()
			return
		}
		natInstance := nmgr.nat
		worker.AddChild(natInstance.Process())
		nmgr.natmu.Unlock()

		// `mappingNotSeenCount` keeps how many times a mapping is configured, but not observed.
		// If a mapping is not observed for consecutive `closeMappingThreshold` times, close it.
		mappingNotSeenCount := make(map[string]int)

		ticker := time.NewTicker(inat.MappingDuration)
		for {
			select {
			case <-worker.Closing():
				ticker.Stop()
				return
			case <-ticker.C:
			}

			listened, listenedErr := nmgr.net.InterfaceListenAddresses()
			if listenedErr != nil {
				log.Infof("Failed to get interface listen addresses: %v", listenedErr)
				continue
			}
			// get local IP addresses
			localIPs := parseLocalIPs(listened)
			// get NAT mappings we already configured
			mapped := nmgr.nat.Mappings()
			// get observed addresses from peers
			observed := nmgr.idService.OwnObservedAddrs()
			// portsToMap are those external ports observed by peers, but not configured
			portsToMap := parseExtraPorts(observed, mapped, localIPs)
			// mappingsToClose are ports configured, but not observed by peers
			mappingsToClose := parseExcessivePorts(observed, mapped)

			log.Debugf("observed %+v, listened %+v, listenedErr %+v, portsToMap %+v\n",
				observed, listened, listenedErr, portsToMap)

			// remove observed addresses from `mappingNotSeenCount` to prevent them from being deleted
			for _, addr := range observed {
				delete(mappingNotSeenCount, addr.String())
			}
			// maintain `mappingNotSeenCount`, close mappings not observed more than `closeMappingThreshold` times
			for _, m := range mappingsToClose {
				externalAddr, err := m.ExternalAddr()
				if err != nil {
					continue
				}
				mappingNotSeenCount[externalAddr.String()] += 1
				if mappingNotSeenCount[externalAddr.String()] > closeMappingThreshold {
					m.Close()
					delete(mappingNotSeenCount, externalAddr.String())
				}
			}
			// add port mappings for `portsToMap`
			for _, port := range portsToMap {
				for _, listenAddr := range nmgr.net.ListenAddresses() {
					addPortMapping(nmgr, listenAddr, port)
				}
			}
		}
	})
}

// NAT returns the natManager's nat object. this may be nil, if
// (a) the search process is still ongoing, or (b) the search process
// found no nat. Clients must check whether the return value is nil.
func (nmgr *natManager) NAT() *inat.NAT {
	nmgr.natmu.Lock()
	defer nmgr.natmu.Unlock()
	return nmgr.nat
}

func addPortMapping(nmgr *natManager, intaddr ma.Multiaddr, externalPort int) {
	nat := nmgr.NAT()
	if nat == nil {
		panic("natManager addPortMapping called without a nat.")
	}

	// first, check if the port mapping already exists.
	if externalPort == inat.RandomMappingExternalPort {
		for _, mapping := range nat.Mappings() {
			if mapping.InternalAddr().Equal(intaddr) {
				return // it exists! return.
			}
		}
	}

	ctx := context.TODO()
	lm := make(lgbl.DeferredMap)
	lm["internalAddr"] = func() interface{} { return intaddr.String() }

	defer log.EventBegin(ctx, "natMgrAddPortMappingWait", lm).Done()

	select {
	case <-nmgr.proc.Closing():
		lm["outcome"] = "cancelled"
		return // no use.
	case <-nmgr.ready: // wait until it's ready.
	}

	// actually start the port map (sub-event because waiting may take a while)
	defer log.EventBegin(ctx, "natMgrAddPortMapping", lm).Done()

	// get the nat
	m, err := nat.NewMapping(intaddr, externalPort)
	if err != nil {
		lm["outcome"] = "failure"
		lm["error"] = err
		return
	}

	extaddr, err := m.ExternalAddr()
	if err != nil {
		lm["outcome"] = "failure"
		lm["error"] = err
		return
	}

	lm["outcome"] = "success"
	lm["externalAddr"] = func() interface{} { return extaddr.String() }
	log.Infof("established nat port mapping: %s <--> %s", intaddr, extaddr)
}

func rmPortMapping(nmgr *natManager, intaddr ma.Multiaddr) {
	nat := nmgr.NAT()
	if nat == nil {
		panic("natManager rmPortMapping called without a nat.")
	}

	// list the port mappings (it may be gone on it's own, so we need to
	// check this list, and not store it ourselves behind the scenes)

	// close mappings for this internal address.
	for _, mapping := range nat.Mappings() {
		if mapping.InternalAddr().Equal(intaddr) {
			mapping.Close()
		}
	}
}

// nmgrNetNotifiee implements the network notification listening part
// of the natManager. this is merely listening to Listen() and ListenClose()
// events.
type nmgrNetNotifiee natManager

func (nn *nmgrNetNotifiee) natManager() *natManager {
	return (*natManager)(nn)
}

func (nn *nmgrNetNotifiee) Listen(n inet.Network, addr ma.Multiaddr) {
	if nn.natManager().NAT() == nil {
		return // not ready or doesnt exist.
	}

	addPortMapping(nn.natManager(), addr, inat.RandomMappingExternalPort)
}

func (nn *nmgrNetNotifiee) ListenClose(n inet.Network, addr ma.Multiaddr) {
	if nn.natManager().NAT() == nil {
		return // not ready or doesnt exist.
	}

	rmPortMapping(nn.natManager(), addr)
}

func (nn *nmgrNetNotifiee) Connected(inet.Network, inet.Conn)      {}
func (nn *nmgrNetNotifiee) Disconnected(inet.Network, inet.Conn)   {}
func (nn *nmgrNetNotifiee) OpenedStream(inet.Network, inet.Stream) {}
func (nn *nmgrNetNotifiee) ClosedStream(inet.Network, inet.Stream) {}
