package main

import (
	metrics "github.com/ipfs/go-libp2p/p2p/metrics"
	tu "github.com/ipfs/go-libp2p/testutil"
	ma "github.com/jbenet/go-multiaddr"
	swarm "go-libp2p/p2p/net/swarm"
	peer "go-libp2p/p2p/peer"
	context "golang.org/x/net/context"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	laddr, err := ma.NewMultiaddr("/ip4/0.0.0.0/tcp/0")
	if err != nil {
		panic(err)
	}

	id, err := tu.RandIdentity()
	if err != nil {
		panic(err)
	}

	ps := peer.NewPeerstore()

	bwc := metrics.NewBandwidthCounter()

	netw, err := swarm.NewNetwork(ctx, []ma.Multiaddr{laddr}, id.ID(), ps, bwc)
	if err != nil {
		panic(err)
	}

}
