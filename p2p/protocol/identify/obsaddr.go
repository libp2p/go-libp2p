package identify

import (
	"sync"
	"time"

	net "github.com/libp2p/go-libp2p-net"
	pstore "github.com/libp2p/go-libp2p-peerstore"
	ma "github.com/multiformats/go-multiaddr"
)

const ActivationThresh = 4

type observation struct {
	seenTime      time.Time
	connDirection net.Direction
}

// ObservedAddr is an entry for an address reported by our peers.
// We only use addresses that:
// - have been observed at least 4 times in last 1h. (counter symmetric nats)
// - have been observed at least once recently (1h), because our position in the
//   network, or network port mapppings, may have changed.
type ObservedAddr struct {
	Addr     ma.Multiaddr
	SeenBy   map[string]observation // peer(observer) address -> observation info
	LastSeen time.Time
}

func (oa *ObservedAddr) activated(ttl time.Duration) bool {
	// cleanup SeenBy set
	now := time.Now()

	for k, ob := range oa.SeenBy {
		if now.Sub(ob.seenTime) > ttl*ActivationThresh {
			delete(oa.SeenBy, k)
		}
	}

	// We only activate if in the TTL other peers observed the same address
	// of ours at least 4 times.
	return len(oa.SeenBy) >= ActivationThresh
}

// ObservedAddrSet keeps track of a set of ObservedAddrs
// the zero-value is ready to be used.
type ObservedAddrSet struct {
	sync.Mutex // guards whole datastruct.

	// local(internal) address -> list of observed(external) addresses
	addrs map[string][]*ObservedAddr
	ttl   time.Duration
}

// AddrsFor return all activated observed addresses associated with the given
// (resolved) listen address.
func (oas *ObservedAddrSet) AddrsFor(addr ma.Multiaddr) (addrs []ma.Multiaddr) {
	oas.Lock()
	defer oas.Unlock()

	// for zero-value.
	if len(oas.addrs) == 0 {
		return nil
	}

	key := string(addr.Bytes())
	observedAddrs, ok := oas.addrs[key]
	if !ok {
		return
	}

	now := time.Now()
	filteredAddrs := make([]*ObservedAddr, 0, len(observedAddrs))
	for _, a := range observedAddrs {
		// leave only alive observed addresses
		if now.Sub(a.LastSeen) <= oas.ttl {
			filteredAddrs = append(filteredAddrs, a)
			if a.activated(oas.ttl) {
				addrs = append(addrs, a.Addr)
			}
		}
	}
	if len(filteredAddrs) > 0 {
		oas.addrs[key] = filteredAddrs
	} else {
		delete(oas.addrs, key)
	}

	return addrs
}

// Addrs return all activated observed addresses
func (oas *ObservedAddrSet) Addrs() (addrs []ma.Multiaddr) {
	oas.Lock()
	defer oas.Unlock()

	// for zero-value.
	if len(oas.addrs) == 0 {
		return nil
	}

	now := time.Now()
	for local, observedAddrs := range oas.addrs {
		filteredAddrs := make([]*ObservedAddr, 0, len(observedAddrs))
		for _, a := range observedAddrs {
			// leave only alive observed addresses
			if now.Sub(a.LastSeen) <= oas.ttl {
				filteredAddrs = append(filteredAddrs, a)
				if a.activated(oas.ttl) {
					addrs = append(addrs, a.Addr)
				}
			}
		}
		oas.addrs[local] = filteredAddrs
	}
	return addrs
}

func (oas *ObservedAddrSet) Add(observed, local, observer ma.Multiaddr,
	direction net.Direction) {

	oas.Lock()
	defer oas.Unlock()

	// for zero-value.
	if oas.addrs == nil {
		oas.addrs = make(map[string][]*ObservedAddr)
		oas.ttl = pstore.OwnObservedAddrTTL
	}

	now := time.Now()
	observerString := observerGroup(observer)
	localString := string(local.Bytes())
	ob := observation{
		seenTime:      now,
		connDirection: direction,
	}

	observedAddrs := oas.addrs[localString]
	// check if observed address seen yet, if so, update it
	for i, previousObserved := range observedAddrs {
		if previousObserved.Addr.Equal(observed) {
			observedAddrs[i].SeenBy[observerString] = ob
			observedAddrs[i].LastSeen = now
			return
		}
	}
	// observed address not seen yet, append it
	oas.addrs[localString] = append(oas.addrs[localString], &ObservedAddr{
		Addr: observed,
		SeenBy: map[string]observation{
			observerString: ob,
		},
		LastSeen: now,
	})
}

// observerGroup is a function that determines what part of
// a multiaddr counts as a different observer. for example,
// two ipfs nodes at the same IP/TCP transport would get
// the exact same NAT mapping; they would count as the
// same observer. This may protect against NATs who assign
// different ports to addresses at different IP hosts, but
// not TCP ports.
//
// Here, we use the root multiaddr address. This is mostly
// IP addresses. In practice, this is what we want.
func observerGroup(m ma.Multiaddr) string {
	//TODO: If IPv6 rolls out we should mark /64 routing zones as one group
	return ma.Split(m)[0].String()
}

func (oas *ObservedAddrSet) SetTTL(ttl time.Duration) {
	oas.Lock()
	defer oas.Unlock()
	oas.ttl = ttl
}

func (oas *ObservedAddrSet) TTL() time.Duration {
	oas.Lock()
	defer oas.Unlock()
	// for zero-value.
	if oas.addrs == nil {
		oas.ttl = pstore.OwnObservedAddrTTL
	}
	return oas.ttl
}
