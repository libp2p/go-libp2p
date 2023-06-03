package peerstore

import (
	"context"

	"github.com/libp2p/go-libp2p/core/peer"
	pstore "github.com/libp2p/go-libp2p/core/peerstore"
)

func PeerInfos(ctx context.Context, ps pstore.Peerstore, peers peer.IDSlice) []peer.AddrInfo {
	pi := make([]peer.AddrInfo, len(peers))
	for i, p := range peers {
		pi[i] = ps.PeerInfo(ctx, p)
	}
	return pi
}

func PeerInfoIDs(pis []peer.AddrInfo) peer.IDSlice {
	ps := make(peer.IDSlice, len(pis))
	for i, pi := range pis {
		ps[i] = pi.ID
	}
	return ps
}
