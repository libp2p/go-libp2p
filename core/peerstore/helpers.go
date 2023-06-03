package peerstore

import (
	"context"

	"github.com/libp2p/go-libp2p/core/peer"
)

// AddrInfos returns an AddrInfo for each specified peer ID, in-order.
func AddrInfos(ctx context.Context, ps Peerstore, peers []peer.ID) []peer.AddrInfo {
	pi := make([]peer.AddrInfo, len(peers))
	for i, p := range peers {
		pi[i] = ps.PeerInfo(ctx, p)
	}
	return pi
}
