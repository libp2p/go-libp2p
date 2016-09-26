package testutil

import (
	"testing"

	pstore "github.com/ipfs/go-libp2p-peerstore"
	bhost "github.com/libp2p/go-libp2p/p2p/host/basic"
	metrics "github.com/libp2p/go-libp2p/p2p/metrics"
	inet "github.com/libp2p/go-libp2p/p2p/net"
	swarm "github.com/libp2p/go-libp2p/p2p/net/swarm"
	tu "github.com/libp2p/go-libp2p/testutil"

	ma "github.com/multiformats/go-multiaddr"
	context "golang.org/x/net/context"
)

func GenSwarmNetwork(t *testing.T, ctx context.Context) *swarm.Network {
	p := tu.RandPeerNetParamsOrFatal(t)
	ps := pstore.NewPeerstore()
	ps.AddPubKey(p.ID, p.PubKey)
	ps.AddPrivKey(p.ID, p.PrivKey)
	n, err := swarm.NewNetwork(ctx, []ma.Multiaddr{p.Addr}, p.ID, ps, metrics.NewBandwidthCounter())
	if err != nil {
		t.Fatal(err)
	}
	ps.AddAddrs(p.ID, n.ListenAddresses(), pstore.PermanentAddrTTL)
	return n
}

func DivulgeAddresses(a, b inet.Network) {
	id := a.LocalPeer()
	addrs := a.Peerstore().Addrs(id)
	b.Peerstore().AddAddrs(id, addrs, pstore.PermanentAddrTTL)
}

func GenHostSwarm(t *testing.T, ctx context.Context) *bhost.BasicHost {
	n := GenSwarmNetwork(t, ctx)
	return bhost.New(n)
}

var RandPeerID = tu.RandPeerID
