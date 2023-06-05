// This separate testing package helps to resolve a circular dependency potentially
// being created between libp2p and libp2p-autonat
package autonattest

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/host/autonat"

	"github.com/stretchr/testify/require"
)

func TestAutonatRoundtrip(t *testing.T) {
	t.Skip("this test doesn't work")

	// 3 hosts are used: [client] and [service + dialback dialer]
	client, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.EnableNATService())
	require.NoError(t, err)
	service, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	require.NoError(t, err)
	dialback, err := libp2p.New(libp2p.NoListenAddrs)
	require.NoError(t, err)
	if _, err := autonat.New(service, autonat.EnableService(dialback.Network())); err != nil {
		t.Fatal(err)
	}

	client.Peerstore().AddAddrs(service.ID(), service.Addrs(), time.Hour)
	require.NoError(t, client.Connect(context.Background(), service.Peerstore().PeerInfo(service.ID())))

	cSub, err := client.EventBus().Subscribe(new(event.EvtLocalReachabilityChanged))
	require.NoError(t, err)
	defer cSub.Close()

	select {
	case stat := <-cSub.Out():
		if stat == network.ReachabilityUnknown {
			t.Fatalf("After status update, client did not know its status")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("sub timed out.")
	}
}
