package main

import (
	context "context"
	metrics "go-libp2p/p2p/metrics"
	swarm "go-libp2p/p2p/net/swarm"
	peer "go-libp2p/p2p/peer"
	tu "go-libp2p/testutil"
	ma "go-multiaddr"
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
