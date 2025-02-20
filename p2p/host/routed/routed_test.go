package routedhost

import (
	"context"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	basic "github.com/libp2p/go-libp2p/p2p/host/basic"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

var _ Routing = (*mockRouting)(nil)

type mockRouting struct {
	callCount  int
	findPeerFn func(ctx context.Context, id peer.ID) (peer.AddrInfo, error)
}

func (m *mockRouting) FindPeer(ctx context.Context, pid peer.ID) (peer.AddrInfo, error) {
	m.callCount++
	return m.findPeerFn(ctx, pid)
}

func TestRoutedHostConnectToObsoleteAddresses(t *testing.T) {
	h1, err := basic.NewHost(swarmt.GenSwarm(t), nil)
	require.NoError(t, err)
	defer h1.Close()
	h1.Start()

	h2, err := basic.NewHost(swarmt.GenSwarm(t), nil)
	require.NoError(t, err)
	defer h2.Close()
	h2.Start()

	// assemble the AddrInfo struct to use for the connection attempt
	pi := peer.AddrInfo{
		ID: h2.ID(),
		// Use a wrong multi address for host 2, so that the initial connection attempt will fail
		// (we have obsolete, old multi address information)
		Addrs: []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/1234")},
	}

	// Build mock routing module and replace the FindPeer function.
	// Now, that function will return the correct multi addresses for host 2
	// (we have fetched the most up-to-date data from the DHT)
	mr := &mockRouting{
		findPeerFn: func(context.Context, peer.ID) (peer.AddrInfo, error) {
			return peer.AddrInfo{
				ID:    h2.ID(),
				Addrs: h2.Addrs(),
			}, nil
		},
	}

	// Build routed host
	rh := Wrap(h1, mr)
	// Connection establishment should have worked without an error
	require.NoError(t, rh.Connect(context.Background(), pi))
	require.Equal(t, 1, mr.callCount, "the mocked FindPeer function should have been called")
}

func TestRoutedHostConnectFindPeerNoUsefulAddrs(t *testing.T) {
	h1, err := basic.NewHost(swarmt.GenSwarm(t), nil)
	require.NoError(t, err)
	defer h1.Close()

	h2, err := basic.NewHost(swarmt.GenSwarm(t), nil)
	require.NoError(t, err)
	defer h2.Close()

	// assemble the AddrInfo struct to use for the connection attempt
	pi := peer.AddrInfo{
		ID: h2.ID(),
		// Use a wrong multi address for host 2, so that the initial connection attempt will fail
		// (we have obsolete, old multi address information)
		Addrs: []ma.Multiaddr{ma.StringCast("/ip4/127.0.0.1/tcp/1234")},
	}

	// Build mock routing module and replace the FindPeer function.
	// Now, that function will return the correct multi addresses for host 2
	// (we have fetched the most up-to-date data from the DHT)
	mr := &mockRouting{findPeerFn: func(context.Context, peer.ID) (peer.AddrInfo, error) { return pi, nil }}

	// Build routed host
	rh := Wrap(h1, mr)
	// Connection establishment should fail, since we didn't provide any useful addresses in FindPeer.
	require.Error(t, rh.Connect(context.Background(), pi))
	require.Equal(t, 1, mr.callCount, "the mocked FindPeer function should have been called")
}
