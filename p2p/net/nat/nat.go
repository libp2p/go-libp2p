package nat

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	logging "github.com/libp2p/go-libp2p/gologshim"

	"github.com/libp2p/go-libp2p/p2p/net/nat/internal/nat"
)

// ErrNoMapping signals no mapping exists for an address
var ErrNoMapping = errors.New("mapping not established")

var log = logging.Logger("nat")

// MappingDuration is a default port mapping duration.
// Port mappings are renewed every (MappingDuration / 3)
const MappingDuration = time.Minute

// CacheTime is the time a mapping will cache an external address for
const CacheTime = 15 * time.Second

// DiscoveryTimeout is the maximum time to wait for NAT discovery.
// This is based on the underlying UPnP and NAT-PMP/PCP protocols:
//   - SSDP (UPnP discovery) waits 5 seconds for responses
//   - NAT-PMP uses exponential backoff starting at 250ms, up to 9 retries
//     (total ~32 seconds if exhausted, but typically responds in 1-2 seconds)
//   - PCP follows similar timing to NAT-PMP
//   - 10 seconds covers common cases while failing fast when no NAT exists
const DiscoveryTimeout = 10 * time.Second

// rediscoveryThreshold is the number of consecutive connection failures
// before triggering NAT rediscovery. We ignore first few failures to
// distinguish between transient network issues and persistent router
// problems like restarts or port changes that require finding the NAT device again.
const rediscoveryThreshold = 3

type entry struct {
	protocol string
	port     int
}

// so we can mock it in tests
var discoverGateway = nat.DiscoverGateway

// DiscoverNAT looks for a NAT device in the network and returns an object that can manage port mappings.
func DiscoverNAT(ctx context.Context) (*NAT, error) {
	natInstance, err := discoverGateway(ctx)
	if err != nil {
		return nil, err
	}

	extAddr := getExternalAddress(natInstance)

	// Log the device addr.
	addr, err := natInstance.GetDeviceAddress()
	if err != nil {
		log.Warn("DiscoverGateway address error", "err", err)
	} else {
		log.Info("DiscoverGateway address", "address", addr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	nat := &NAT{
		nat:       natInstance,
		mappings:  make(map[entry]int),
		ctx:       ctx,
		ctxCancel: cancel,
	}
	nat.extAddr.Store(&extAddr)
	nat.refCount.Add(1)
	go func() {
		defer nat.refCount.Done()
		nat.background()
	}()
	return nat, nil
}

// NAT is an object that manages address port mappings in
// NATs (Network Address Translators). It is a long-running
// service that will periodically renew port mappings,
// and keep an up-to-date list of all the external addresses.
//
// Locking strategy:
//   - natmu: Protects nat instance and rediscovery state (nat, consecutiveFailures, rediscovering)
//   - mappingmu: Protects port mappings table and closed flag (mappings, closed)
//   - Lock ordering: When both locks are needed, always acquire mappingmu before natmu
//     to prevent deadlocks
//   - We use separate mutexes because the NAT instance may change (e.g., when router
//     restarts and UPnP port changes), but the port mappings must persist and be
//     re-applied across all instances. This separation allows the mappings table to
//     remain stable while the underlying NAT device changes.
type NAT struct {
	natmu sync.Mutex
	nat   nat.NAT

	// Track connection failures for auto-rediscovery
	consecutiveFailures int  // protected by natmu
	rediscovering       bool // protected by natmu

	// External IP of the NAT. Will be renewed periodically (every CacheTime).
	extAddr atomic.Pointer[netip.Addr]

	refCount  sync.WaitGroup
	ctx       context.Context
	ctxCancel context.CancelFunc

	// Port mappings that should persist across NAT instance changes
	mappingmu sync.RWMutex
	closed    bool          // protected by mappingmu
	mappings  map[entry]int // protected by mappingmu
}

// Close shuts down all port mappings. NAT can no longer be used.
func (nat *NAT) Close() error {
	nat.mappingmu.Lock()
	nat.closed = true
	nat.mappingmu.Unlock()

	nat.ctxCancel()
	nat.refCount.Wait()
	return nil
}

func (nat *NAT) GetMapping(protocol string, port int) (addr netip.AddrPort, found bool) {
	nat.mappingmu.Lock()
	defer nat.mappingmu.Unlock()

	if !nat.extAddr.Load().IsValid() {
		return netip.AddrPort{}, false
	}
	extPort, found := nat.mappings[entry{protocol: protocol, port: port}]
	// The mapping may have an invalid port.
	if !found || extPort == 0 {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(*nat.extAddr.Load(), uint16(extPort)), true
}

// AddMapping attempts to construct a mapping on protocol and internal port.
// It blocks until a mapping was established. Once added, it periodically renews the mapping.
//
// May not succeed, and mappings may change over time;
// NAT devices may not respect our port requests, and even lie.
func (nat *NAT) AddMapping(ctx context.Context, protocol string, port int) error {
	switch protocol {
	case "tcp", "udp":
	default:
		return fmt.Errorf("invalid protocol: %s", protocol)
	}

	nat.mappingmu.Lock()
	defer nat.mappingmu.Unlock()

	if nat.closed {
		return errors.New("closed")
	}

	// Check if the mapping already exists to avoid duplicate work
	e := entry{protocol: protocol, port: port}
	if _, exists := nat.mappings[e]; exists {
		return nil
	}

	log.Info("Starting maintenance of port mapping", "protocol", protocol, "port", port)

	// do it once synchronously, so first mapping is done right away, and before exiting,
	// allowing users -- in the optimistic case -- to use results right after.
	extPort := nat.establishMapping(ctx, protocol, port)
	// Don't validate the mapping here, we refresh the mappings based on this map.
	// We can try getting a port again in case it succeeds. In the worst case,
	// this is one extra LAN request every few minutes.
	nat.mappings[e] = extPort
	return nil
}

// RemoveMapping removes a port mapping.
// It blocks until the NAT has removed the mapping.
func (nat *NAT) RemoveMapping(ctx context.Context, protocol string, port int) error {
	nat.mappingmu.Lock()
	defer nat.mappingmu.Unlock()
	nat.natmu.Lock()
	defer nat.natmu.Unlock()

	switch protocol {
	case "tcp", "udp":
		e := entry{protocol: protocol, port: port}
		if _, ok := nat.mappings[e]; ok {
			log.Info("Stopping maintenance of port mapping", "protocol", protocol, "port", port)
			delete(nat.mappings, e)
			return nat.nat.DeletePortMapping(ctx, protocol, port)
		}
		return errors.New("unknown mapping")
	default:
		return fmt.Errorf("invalid protocol: %s", protocol)
	}
}

func (nat *NAT) background() {
	// Renew port mappings every 20 seconds (1/3 of 60s lifetime).
	// - NAT-PMP RFC 6886 recommends renewing at 50% of lifetime
	// - We use 33% for added safety against silent lifetime reductions
	// NOTE: This aggressive 60s/20s pattern may be outdated for modern routers
	// but provides quick cleanup and fast failure detection for our rediscovery.
	// TODO: Research longer durations (e.g. 30min/10min) to reduce router load
	const mappingUpdate = MappingDuration / 3

	now := time.Now()
	nextMappingUpdate := now.Add(mappingUpdate)
	nextAddrUpdate := now.Add(CacheTime)

	t := time.NewTimer(minTime(nextMappingUpdate, nextAddrUpdate).Sub(now)) // don't use a ticker here. We don't know how long establishing the mappings takes.
	defer t.Stop()

	var in []entry
	var out []int // port numbers
	for {
		select {
		case now := <-t.C:
			if now.After(nextMappingUpdate) {
				in = in[:0]
				out = out[:0]
				nat.mappingmu.Lock()
				for e := range nat.mappings {
					in = append(in, e)
				}
				nat.mappingmu.Unlock()
				// Establishing the mapping involves network requests.
				// Don't hold the mutex, just save the ports.
				for _, e := range in {
					out = append(out, nat.establishMapping(nat.ctx, e.protocol, e.port))
				}
				nat.mappingmu.Lock()
				for i, p := range in {
					if _, ok := nat.mappings[p]; !ok {
						continue // entry might have been deleted
					}
					nat.mappings[p] = out[i]
				}
				nat.mappingmu.Unlock()
				nextMappingUpdate = time.Now().Add(mappingUpdate)
			}
			if now.After(nextAddrUpdate) {
				nat.natmu.Lock()
				extIP, err := nat.nat.GetExternalAddress()
				nat.natmu.Unlock()

				var extAddr netip.Addr
				if err == nil {
					extAddr, _ = netip.AddrFromSlice(extIP)
				}
				nat.extAddr.Store(&extAddr)
				nextAddrUpdate = time.Now().Add(CacheTime)
			}
			t.Reset(time.Until(minTime(nextAddrUpdate, nextMappingUpdate)))
		case <-nat.ctx.Done():
			nat.mappingmu.Lock()
			defer nat.mappingmu.Unlock()
			nat.natmu.Lock()
			defer nat.natmu.Unlock()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			for e := range nat.mappings {
				nat.nat.DeletePortMapping(ctx, e.protocol, e.port)
			}
			clear(nat.mappings)
			return
		}
	}
}

func (nat *NAT) establishMapping(ctx context.Context, protocol string, internalPort int) (externalPort int) {
	log.Debug("Attempting port map", "protocol", protocol, "internal_port", internalPort)
	const comment = "libp2p"

	// Try to establish the mapping with both NAT calls under the same lock
	nat.natmu.Lock()
	defer nat.natmu.Unlock()

	var err error
	externalPort, err = nat.nat.AddPortMapping(ctx, protocol, internalPort, comment, MappingDuration)
	if err != nil {
		// Some hardware does not support mappings with timeout, so try that
		externalPort, err = nat.nat.AddPortMapping(ctx, protocol, internalPort, comment, 0)
	}

	// Handle success
	if err == nil && externalPort != 0 {
		nat.consecutiveFailures = 0
		log.Debug("NAT port mapping established", "protocol", protocol, "internal_port", internalPort, "external_port", externalPort)
		return externalPort
	}

	// Handle failures
	if err != nil {
		log.Warn("NAT port mapping failed", "protocol", protocol, "internal_port", internalPort, "err", err)

		// Check if this is a connection error that might indicate router restart
		// See: https://github.com/libp2p/go-libp2p/issues/3224#issuecomment-2866844723
		// Note: We use string matching because goupnp doesn't preserve error chains (uses %v instead of %w)
		if strings.Contains(err.Error(), "connection refused") {
			nat.consecutiveFailures++
			if nat.consecutiveFailures >= rediscoveryThreshold && !nat.rediscovering {
				nat.rediscovering = true
				// Spawn in goroutine to avoid blocking the caller while we
				// perform network discovery, which can take up to 30 seconds.
				// The rediscovering flag prevents multiple concurrent attempts.
				go nat.rediscoverNAT()
			}
		} else {
			// Reset counter for non-connection errors (transient failures)
			nat.consecutiveFailures = 0
		}
		return 0
	}

	// externalPort is 0 but no error was returned
	log.Warn("NAT port mapping failed", "protocol", protocol, "internal_port", internalPort, "external_port", 0)
	return 0
}

// rediscoverNAT attempts to rediscover the NAT device after connection failures
func (nat *NAT) rediscoverNAT() {
	log.Info("NAT rediscovery triggered due to repeated connection failures")

	ctx, cancel := context.WithTimeout(nat.ctx, DiscoveryTimeout)
	defer cancel()

	newNATInstance, err := discoverGateway(ctx)
	if err != nil {
		log.Warn("NAT rediscovery failed", "err", err)
		nat.natmu.Lock()
		defer nat.natmu.Unlock()
		nat.rediscovering = false
		return
	}

	extAddr := getExternalAddress(newNATInstance)

	// Replace the NAT instance
	// No cleanup of the old instance needed because:
	// - Router restart has already wiped all mappings
	// - Old UPnP endpoint is dead (connection refused)
	// - If router didn't actually restart (false positive), any stale mappings
	//   on the router expire naturally (60 second UPnP timeout)
	nat.natmu.Lock()
	nat.nat = newNATInstance
	nat.extAddr.Store(&extAddr)
	nat.consecutiveFailures = 0
	nat.rediscovering = false
	nat.natmu.Unlock()

	// Re-establish all existing mappings on the new NAT instance
	nat.mappingmu.Lock()
	for e := range nat.mappings {
		extPort := nat.establishMapping(nat.ctx, e.protocol, e.port)
		nat.mappings[e] = extPort
		if extPort != 0 {
			log.Info("NAT mapping restored after rediscovery", "protocol", e.protocol, "internal_port", e.port, "external_port", extPort)
		}
	}
	nat.mappingmu.Unlock()

	log.Info("NAT rediscovery successful")
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// getExternalAddress retrieves and parses the external address from a NAT instance
func getExternalAddress(natInstance nat.NAT) netip.Addr {
	extIP, err := natInstance.GetExternalAddress()
	if err != nil {
		log.Debug("Failed to get external address", "err", err)
		return netip.Addr{}
	}
	extAddr, ok := netip.AddrFromSlice(extIP)
	if !ok {
		log.Debug("Failed to parse external address", "ip", extIP)
		return netip.Addr{}
	}
	return extAddr
}
