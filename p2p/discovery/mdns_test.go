package discovery

import (
	"context"
	"testing"
	"time"

	bhost "github.com/libp2p/go-libp2p/p2p/host/basic"

	host "github.com/libp2p/go-libp2p-host"
	pstore "github.com/libp2p/go-libp2p-peerstore"
	swarmt "github.com/libp2p/go-libp2p-swarm/testing"
	ma "github.com/multiformats/go-multiaddr"
)

type DiscoveryNotifee struct {
	h host.Host
}

func (n *DiscoveryNotifee) HandlePeerFound(pi pstore.PeerInfo) {
	n.h.Connect(context.Background(), pi)
}

func TestGetBestPort(t *testing.T) {
	port, err := getBestPort([]ma.Multiaddr{ma.StringCast("/ip4/1.2.3.4/tcp/2222"), ma.StringCast("/ip4/0.0.0.0/tcp/1234")})
	if err != nil {
		t.Fatal(err)
	}
	if port != 1234 {
		t.Errorf("expected port 1234, got port %d", port)
	}

	port, err = getBestPort([]ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/2222"), ma.StringCast("/ip4/0.0.0.0/tcp/1234")})
	if err != nil {
		t.Fatal(err)
	}
	if port != 1234 {
		t.Errorf("expected port 1234, got port %d", port)
	}

	port, err = getBestPort([]ma.Multiaddr{ma.StringCast("/ip4/1.2.3.4/tcp/2222"), ma.StringCast("/ip4/127.0.0.1/tcp/1234")})
	if err != nil {
		t.Fatal(err)
	}
	if port != 2222 {
		t.Errorf("expected port 2222, got port %d", port)
	}
	port, err = getBestPort([]ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/1234"), ma.StringCast("/ip4/1.2.3.4/tcp/2222")})
	if err != nil {
		t.Fatal(err)
	}
	if port != 2222 {
		t.Errorf("expected port 2222, got port %d", port)
	}
	port, err = getBestPort([]ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/1234")})
	if err != nil {
		t.Fatal(err)
	}
	if port != 1234 {
		t.Errorf("expected port 1234, got port %d", port)
	}

	_, err = getBestPort([]ma.Multiaddr{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestMdnsDiscovery(t *testing.T) {
	//TODO: re-enable when the new lib will get integrated
	t.Skip("TestMdnsDiscovery fails randomly with current lib")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := bhost.New(swarmt.GenSwarm(t, ctx))
	b := bhost.New(swarmt.GenSwarm(t, ctx))

	sa, err := NewMdnsService(ctx, a, time.Second, "someTag")
	if err != nil {
		t.Fatal(err)
	}

	sb, err := NewMdnsService(ctx, b, time.Second, "someTag")
	if err != nil {
		t.Fatal(err)
	}

	_ = sb

	n := &DiscoveryNotifee{a}

	sa.RegisterNotifee(n)

	time.Sleep(time.Second * 2)

	err = a.Connect(ctx, pstore.PeerInfo{ID: b.ID()})
	if err != nil {
		t.Fatal(err)
	}
}
