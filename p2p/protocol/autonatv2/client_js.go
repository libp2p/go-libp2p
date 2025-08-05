//go:build js
// +build js

package autonatv2

import (
	libp2pwebtransport "github.com/libp2p/go-libp2p/p2p/transport/webtransport"
	ma "github.com/multiformats/go-multiaddr"
)

// normalizeMultiaddr returns a multiaddr suitable for equality checks.
// If the multiaddr is a webtransport component, it removes the certhashes.
func normalizeMultiaddr(addr ma.Multiaddr) ma.Multiaddr {
	ok, n := libp2pwebtransport.IsWebtransportMultiaddr(addr)

	if ok && n > 0 {
		out := addr
		for i := 0; i < n; i++ {
			out, _ = ma.SplitLast(out)
		}
		return out
	}
	return addr
}
