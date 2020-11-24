package conngater

import (
	"net"
	"testing"

	"github.com/ipfs/go-datastore"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

func TestConnectionGater(t *testing.T) {
	ds := datastore.NewMapDatastore()

	peerA := peer.ID("A")
	peerB := peer.ID("B")

	ip1 := net.ParseIP("1.2.3.4")

	_, ipNet1, err := net.ParseCIDR("1.2.3.0/24")
	if err != nil {
		t.Fatal(err)
	}

	cg, err := NewBasicConnectionGater(ds)
	if err != nil {
		t.Fatal(err)
	}

	// test peer blocking
	allow := cg.InterceptPeerDial(peerA)
	if !allow {
		t.Fatal("expected gater to allow peerA")
	}

	allow = cg.InterceptPeerDial(peerB)
	if !allow {
		t.Fatal("expected gater to allow peerB")
	}

	allow = cg.InterceptSecured(network.DirInbound, peerA, &mockConnMultiaddrs{local: nil, remote: nil})
	if !allow {
		t.Fatal("expected gater to allow peerA")
	}

	allow = cg.InterceptSecured(network.DirInbound, peerB, &mockConnMultiaddrs{local: nil, remote: nil})
	if !allow {
		t.Fatal("expected gater to allow peerB")
	}

	err = cg.BlockPeer(peerA)
	if err != nil {
		t.Fatal(err)
	}

	allow = cg.InterceptPeerDial(peerA)
	if allow {
		t.Fatal("expected gater to deny peerA")
	}

	allow = cg.InterceptPeerDial(peerB)
	if !allow {
		t.Fatal("expected gater to allow peerB")
	}

	allow = cg.InterceptSecured(network.DirInbound, peerA, &mockConnMultiaddrs{local: nil, remote: nil})
	if allow {
		t.Fatal("expected gater to deny peerA")
	}

	allow = cg.InterceptSecured(network.DirInbound, peerB, &mockConnMultiaddrs{local: nil, remote: nil})
	if !allow {
		t.Fatal("expected gater to allow peerB")
	}

	// test addr and subnet blocking
	allow = cg.InterceptAddrDial(peerB, ma.StringCast("/ip4/1.2.3.4/tcp/1234"))
	if !allow {
		t.Fatal("expected gater to allow peerB in 1.2.3.4")
	}

	allow = cg.InterceptAccept(&mockConnMultiaddrs{local: nil, remote: ma.StringCast("/ip4/1.2.3.4/tcp/1234")})
	if !allow {
		t.Fatal("expected gater to allow peerB in 1.2.3.4")
	}

	allow = cg.InterceptAddrDial(peerB, ma.StringCast("/ip4/1.2.3.5/tcp/1234"))
	if !allow {
		t.Fatal("expected gater to allow peerB in 1.2.3.5")
	}

	allow = cg.InterceptAccept(&mockConnMultiaddrs{local: nil, remote: ma.StringCast("/ip4/1.2.3.5/tcp/1234")})
	if !allow {
		t.Fatal("expected gater to allow peerB in 1.2.3.5")
	}

	allow = cg.InterceptAddrDial(peerB, ma.StringCast("/ip4/2.3.4.5/tcp/1234"))
	if !allow {
		t.Fatal("expected gater to allow peerB in 2.3.4.5")
	}

	allow = cg.InterceptAccept(&mockConnMultiaddrs{local: nil, remote: ma.StringCast("/ip4/2.3.4.5/tcp/1234")})
	if !allow {
		t.Fatal("expected gater to allow peerB in 2.3.4.5")
	}

	err = cg.BlockAddr(ip1)
	if err != nil {
		t.Fatal(err)
	}

	allow = cg.InterceptAddrDial(peerB, ma.StringCast("/ip4/1.2.3.4/tcp/1234"))
	if allow {
		t.Fatal("expected gater to deny peerB in 1.2.3.4")
	}

	allow = cg.InterceptAccept(&mockConnMultiaddrs{local: nil, remote: ma.StringCast("/ip4/1.2.3.4/tcp/1234")})
	if allow {
		t.Fatal("expected gater to deny peerB in 1.2.3.4")
	}

	allow = cg.InterceptAddrDial(peerB, ma.StringCast("/ip4/1.2.3.5/tcp/1234"))
	if !allow {
		t.Fatal("expected gater to allow peerB in 1.2.3.5")
	}

	allow = cg.InterceptAccept(&mockConnMultiaddrs{local: nil, remote: ma.StringCast("/ip4/1.2.3.5/tcp/1234")})
	if !allow {
		t.Fatal("expected gater to allow peerB in 1.2.3.5")
	}

	allow = cg.InterceptAddrDial(peerB, ma.StringCast("/ip4/2.3.4.5/tcp/1234"))
	if !allow {
		t.Fatal("expected gater to allow peerB in 2.3.4.5")
	}

	allow = cg.InterceptAccept(&mockConnMultiaddrs{local: nil, remote: ma.StringCast("/ip4/2.3.4.5/tcp/1234")})
	if !allow {
		t.Fatal("expected gater to allow peerB in 2.3.4.5")
	}

	err = cg.BlockSubnet(ipNet1)
	if err != nil {
		t.Fatal(err)
	}

	allow = cg.InterceptAddrDial(peerB, ma.StringCast("/ip4/1.2.3.5/tcp/1234"))
	if allow {
		t.Fatal("expected gater to deny peerB in 1.2.3.5")
	}

	allow = cg.InterceptAccept(&mockConnMultiaddrs{local: nil, remote: ma.StringCast("/ip4/1.2.3.5/tcp/1234")})
	if allow {
		t.Fatal("expected gater to deny peerB in 1.2.3.5")
	}

	allow = cg.InterceptAddrDial(peerB, ma.StringCast("/ip4/2.3.4.5/tcp/1234"))
	if !allow {
		t.Fatal("expected gater to allow peerB in 2.3.4.5")
	}

	allow = cg.InterceptAccept(&mockConnMultiaddrs{local: nil, remote: ma.StringCast("/ip4/2.3.4.5/tcp/1234")})
	if !allow {
		t.Fatal("expected gater to allow peerB in 2.3.4.5")
	}

	// make a new gater reusing the datastore to test persistence
	cg, err = NewBasicConnectionGater(ds)
	if err != nil {
		t.Fatal(err)
	}

	// test the list methods while at it
	blockedPeers := cg.ListBlockedPeers()
	if len(blockedPeers) != 1 {
		t.Fatalf("expected 1 blocked peer, but got %d", len(blockedPeers))
	}

	blockedAddrs := cg.ListBlockedAddrs()
	if len(blockedAddrs) != 1 {
		t.Fatalf("expected 1 blocked addr, but got %d", len(blockedAddrs))
	}

	blockedSubnets := cg.ListBlockedSubnets()
	if len(blockedSubnets) != 1 {
		t.Fatalf("expected 1 blocked subnet, but got %d", len(blockedSubnets))
	}

	allow = cg.InterceptPeerDial(peerA)
	if allow {
		t.Fatal("expected gater to deny peerA")
	}

	allow = cg.InterceptPeerDial(peerB)
	if !allow {
		t.Fatal("expected gater to allow peerB")
	}

	allow = cg.InterceptSecured(network.DirInbound, peerA, &mockConnMultiaddrs{local: nil, remote: nil})
	if allow {
		t.Fatal("expected gater to deny peerA")
	}

	allow = cg.InterceptSecured(network.DirInbound, peerB, &mockConnMultiaddrs{local: nil, remote: nil})
	if !allow {
		t.Fatal("expected gater to allow peerB")
	}

	allow = cg.InterceptAddrDial(peerB, ma.StringCast("/ip4/1.2.3.4/tcp/1234"))
	if allow {
		t.Fatal("expected gater to deny peerB in 1.2.3.4")
	}

	allow = cg.InterceptAccept(&mockConnMultiaddrs{local: nil, remote: ma.StringCast("/ip4/1.2.3.4/tcp/1234")})
	if allow {
		t.Fatal("expected gater to deny peerB in 1.2.3.4")
	}

	allow = cg.InterceptAddrDial(peerB, ma.StringCast("/ip4/1.2.3.5/tcp/1234"))
	if allow {
		t.Fatal("expected gater to deny peerB in 1.2.3.5")
	}

	allow = cg.InterceptAccept(&mockConnMultiaddrs{local: nil, remote: ma.StringCast("/ip4/1.2.3.5/tcp/1234")})
	if allow {
		t.Fatal("expected gater to deny peerB in 1.2.3.5")
	}

	allow = cg.InterceptAddrDial(peerB, ma.StringCast("/ip4/2.3.4.5/tcp/1234"))
	if !allow {
		t.Fatal("expected gater to allow peerB in 2.3.4.5")
	}

	allow = cg.InterceptAccept(&mockConnMultiaddrs{local: nil, remote: ma.StringCast("/ip4/2.3.4.5/tcp/1234")})
	if !allow {
		t.Fatal("expected gater to allow peerB in 2.3.4.5")
	}

	// undo the blocks to ensure that we can unblock stuff
	err = cg.UnblockPeer(peerA)
	if err != nil {
		t.Fatal(err)
	}

	err = cg.UnblockAddr(ip1)
	if err != nil {
		t.Fatal(err)
	}

	err = cg.UnblockSubnet(ipNet1)
	if err != nil {
		t.Fatal(err)
	}

	allow = cg.InterceptPeerDial(peerA)
	if !allow {
		t.Fatal("expected gater to allow peerA")
	}

	allow = cg.InterceptPeerDial(peerB)
	if !allow {
		t.Fatal("expected gater to allow peerB")
	}

	allow = cg.InterceptSecured(network.DirInbound, peerA, &mockConnMultiaddrs{local: nil, remote: nil})
	if !allow {
		t.Fatal("expected gater to allow peerA")
	}

	allow = cg.InterceptSecured(network.DirInbound, peerB, &mockConnMultiaddrs{local: nil, remote: nil})
	if !allow {
		t.Fatal("expected gater to allow peerB")
	}

	allow = cg.InterceptAddrDial(peerB, ma.StringCast("/ip4/1.2.3.4/tcp/1234"))
	if !allow {
		t.Fatal("expected gater to allow peerB in 1.2.3.4")
	}

	allow = cg.InterceptAccept(&mockConnMultiaddrs{local: nil, remote: ma.StringCast("/ip4/1.2.3.4/tcp/1234")})
	if !allow {
		t.Fatal("expected gater to allow peerB in 1.2.3.4")
	}

	allow = cg.InterceptAddrDial(peerB, ma.StringCast("/ip4/1.2.3.5/tcp/1234"))
	if !allow {
		t.Fatal("expected gater to allow peerB in 1.2.3.5")
	}

	allow = cg.InterceptAccept(&mockConnMultiaddrs{local: nil, remote: ma.StringCast("/ip4/1.2.3.5/tcp/1234")})
	if !allow {
		t.Fatal("expected gater to allow peerB in 1.2.3.5")
	}

	allow = cg.InterceptAddrDial(peerB, ma.StringCast("/ip4/2.3.4.5/tcp/1234"))
	if !allow {
		t.Fatal("expected gater to allow peerB in 2.3.4.5")
	}

	allow = cg.InterceptAccept(&mockConnMultiaddrs{local: nil, remote: ma.StringCast("/ip4/2.3.4.5/tcp/1234")})
	if !allow {
		t.Fatal("expected gater to allow peerB in 2.3.4.5")
	}

	// make a new gater reusing the datastore to test persistence of unblocks
	cg, err = NewBasicConnectionGater(ds)
	if err != nil {
		t.Fatal(err)
	}

	allow = cg.InterceptPeerDial(peerA)
	if !allow {
		t.Fatal("expected gater to allow peerA")
	}

	allow = cg.InterceptPeerDial(peerB)
	if !allow {
		t.Fatal("expected gater to allow peerB")
	}

	allow = cg.InterceptSecured(network.DirInbound, peerA, &mockConnMultiaddrs{local: nil, remote: nil})
	if !allow {
		t.Fatal("expected gater to allow peerA")
	}

	allow = cg.InterceptSecured(network.DirInbound, peerB, &mockConnMultiaddrs{local: nil, remote: nil})
	if !allow {
		t.Fatal("expected gater to allow peerB")
	}

	allow = cg.InterceptAddrDial(peerB, ma.StringCast("/ip4/1.2.3.4/tcp/1234"))
	if !allow {
		t.Fatal("expected gater to allow peerB in 1.2.3.4")
	}

	allow = cg.InterceptAccept(&mockConnMultiaddrs{local: nil, remote: ma.StringCast("/ip4/1.2.3.4/tcp/1234")})
	if !allow {
		t.Fatal("expected gater to allow peerB in 1.2.3.4")
	}

	allow = cg.InterceptAddrDial(peerB, ma.StringCast("/ip4/1.2.3.5/tcp/1234"))
	if !allow {
		t.Fatal("expected gater to allow peerB in 1.2.3.5")
	}

	allow = cg.InterceptAccept(&mockConnMultiaddrs{local: nil, remote: ma.StringCast("/ip4/1.2.3.5/tcp/1234")})
	if !allow {
		t.Fatal("expected gater to allow peerB in 1.2.3.5")
	}

	allow = cg.InterceptAddrDial(peerB, ma.StringCast("/ip4/2.3.4.5/tcp/1234"))
	if !allow {
		t.Fatal("expected gater to allow peerB in 2.3.4.5")
	}

	allow = cg.InterceptAccept(&mockConnMultiaddrs{local: nil, remote: ma.StringCast("/ip4/2.3.4.5/tcp/1234")})
	if !allow {
		t.Fatal("expected gater to allow peerB in 2.3.4.5")
	}
}

type mockConnMultiaddrs struct {
	local, remote ma.Multiaddr
}

func (cma *mockConnMultiaddrs) LocalMultiaddr() ma.Multiaddr {
	return cma.local
}

func (cma *mockConnMultiaddrs) RemoteMultiaddr() ma.Multiaddr {
	return cma.remote
}
