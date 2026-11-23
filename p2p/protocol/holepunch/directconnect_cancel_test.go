package holepunch_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	"github.com/marcopolo/simnet"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	ma "github.com/multiformats/go-multiaddr"
)

// A call whose responder stalls returns as soon as its context is canceled,
// and releases the peer: the next call starts instead of failing with
// ErrHolePunchActive.
func TestDirectConnectContextCancelReleasesPeer(t *testing.T) {
	router := &simnet.SimpleFirewallRouter{}
	relay := MustNewHost(t,
		quicSimnet(true, router),
		libp2p.ListenAddrs(ma.StringCast("/ip4/1.2.0.1/udp/8000/quic-v1")),
		libp2p.DisableRelay(),
		libp2p.ResourceManager(&network.NullResourceManager{}),
		libp2p.WithFxOption(fx.Invoke(func(h host.Host) {
			_, err := relayv2.New(h)
			require.NoError(t, err)
		})),
	)
	h1 := MustNewHost(t,
		quicSimnet(false, router),
		libp2p.ForceReachabilityPrivate(),
		libp2p.ListenAddrs(ma.StringCast("/ip4/2.2.0.1/udp/8000/quic-v1")),
		libp2p.ResourceManager(&network.NullResourceManager{}),
		connectToRelay(&relay),
	)
	h2 := MustNewHost(t,
		quicSimnet(false, router),
		libp2p.ListenAddrs(ma.StringCast("/ip4/2.2.0.2/udp/8001/quic-v1")),
		libp2p.ResourceManager(&network.NullResourceManager{}),
		connectToRelay(&relay),
	)
	defer h1.Close()
	defer h2.Close()
	defer relay.Close()
	time.Sleep(100 * time.Millisecond)

	hps := addHolePunchService(t, h2, []ma.Multiaddr{ma.StringCast("/ip4/2.2.0.2/udp/8001/quic-v1")})
	h2.RemoveStreamHandler(holepunch.Protocol)
	stalled := make(chan struct{})
	h1.SetStreamHandler(holepunch.Protocol, func(s network.Stream) {
		defer s.Reset()
		select {
		case <-stalled:
		case <-time.After(30 * time.Second):
		}
	})
	defer close(stalled)
	require.NoError(t, h2.Connect(context.Background(), peer.AddrInfo{ID: h1.ID(), Addrs: h1.Addrs()}))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- hps.DirectConnectContext(ctx, h1.ID()) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("DirectConnectContext still running a second after its context was canceled")
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel2()
	err := hps.DirectConnectContext(ctx2, h1.ID())
	require.False(t, errors.Is(err, holepunch.ErrHolePunchActive), "the canceled call still holds the peer: %v", err)
}
