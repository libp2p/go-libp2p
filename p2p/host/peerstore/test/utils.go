package test

import (
	"context"
	"fmt"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	pt "github.com/libp2p/go-libp2p/core/test"

	ma "github.com/multiformats/go-multiaddr"
)

func Multiaddr(m string) ma.Multiaddr {
	maddr, err := ma.NewMultiaddr(m)
	if err != nil {
		panic(err)
	}
	return maddr
}

type peerpair struct {
	ID   peer.ID
	Addr []ma.Multiaddr
}

func RandomPeer(b *testing.B, addrCount int) *peerpair {
	var (
		pid   peer.ID
		err   error
		addrs = make([]ma.Multiaddr, addrCount)
		aFmt  = "/ip4/127.0.0.1/tcp/%d/ipfs/%s"
	)

	b.Helper()
	if pid, err = pt.RandPeerID(); err != nil {
		b.Fatal(err)
	}

	for i := 0; i < addrCount; i++ {
		if addrs[i], err = ma.NewMultiaddr(fmt.Sprintf(aFmt, i, pid)); err != nil {
			b.Fatal(err)
		}
	}
	return &peerpair{pid, addrs}
}

func AddressProducer(ctx context.Context, b *testing.B, addrs chan *peerpair, addrsPerPeer int) {
	b.Helper()
	defer close(addrs)
	for {
		p := RandomPeer(b, addrsPerPeer)
		select {
		case addrs <- p:
		case <-ctx.Done():
			return
		}
	}
}

func GenerateAddrs(count int) []ma.Multiaddr {
	var addrs = make([]ma.Multiaddr, count)
	for i := 0; i < count; i++ {
		addrs[i] = Multiaddr(fmt.Sprintf("/ip4/1.1.1.%d/tcp/1111", i))
	}
	return addrs
}

func GeneratePeerIDs(count int) []peer.ID {
	var ids = make([]peer.ID, count)
	for i := 0; i < count; i++ {
		ids[i], _ = pt.RandPeerID()
	}
	return ids
}

func AssertAddressesEqual(t *testing.T, exp, act []ma.Multiaddr) {
	t.Helper()
	if len(exp) != len(act) {
		t.Fatalf("lengths not the same. expected %d, got %d\n", len(exp), len(act))
	}

	for _, a := range exp {
		found := false

		for _, b := range act {
			if a.Equal(b) {
				found = true
				break
			}
		}

		if !found {
			t.Fatalf("expected address %s not found", a)
		}
	}
}
