package autorelay

import (
	ma "github.com/multiformats/go-multiaddr"
)

// Filter filters out all relay addresses.
//
// Deprecated: It is trivial for a user to implement this if they need this.
func Filter(addrs []ma.Multiaddr) []ma.Multiaddr {
	raddrs := make([]ma.Multiaddr, 0, len(addrs))
	for _, addr := range addrs {
		if isRelayAddr(addr) {
			continue
		}
		raddrs = append(raddrs, addr)
	}
	return raddrs
}
