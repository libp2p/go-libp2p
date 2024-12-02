package holepunch

import (
	"context"
	"slices"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	ma "github.com/multiformats/go-multiaddr"
)

func removeRelayAddrs(addrs []ma.Multiaddr) []ma.Multiaddr {
	return slices.DeleteFunc(addrs, isRelayAddress)
}

func isRelayAddress(a ma.Multiaddr) bool {
	_, err := a.ValueForProtocol(ma.P_CIRCUIT)
	return err == nil
}

func addrsToBytes(as []ma.Multiaddr) [][]byte {
	bzs := make([][]byte, 0, len(as))
	for _, a := range as {
		bzs = append(bzs, a.Bytes())
	}
	return bzs
}

func addrsFromBytes(bzs [][]byte) []ma.Multiaddr {
	addrs := make([]ma.Multiaddr, 0, len(bzs))
	for _, bz := range bzs {
		a, err := ma.NewMultiaddrBytes(bz)
		if err == nil {
			addrs = append(addrs, a)
		}
	}
	return addrs
}

func getDirectConnection(h host.Host, p peer.ID) network.Conn {
	for _, c := range h.Network().ConnsToPeer(p) {
		if !isRelayAddress(c.RemoteMultiaddr()) {
			return c
		}
	}
	return nil
}

func holePunchConnect(ctx context.Context, host host.Host, pi peer.AddrInfo, isClient bool) error {
	holePunchCtx := network.WithSimultaneousConnect(ctx, isClient, "hole-punching")
	forceDirectConnCtx := network.WithForceDirectDial(holePunchCtx, "hole-punching")
	dialCtx, cancel := context.WithTimeout(forceDirectConnCtx, dialTimeout)
	defer cancel()

	if err := host.Connect(dialCtx, pi); err != nil {
		log.Debugw("hole punch attempt with peer failed", "peer ID", pi.ID, "error", err)
		return err
	}
	log.Debugw("hole punch successful", "peer", pi.ID)
	return nil
}
