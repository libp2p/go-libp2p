package basichost

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoStreamOverTransientConnection(t *testing.T) {
	h1, err := libp2p.New(
		libp2p.NoListenAddrs,
		libp2p.EnableRelay(),
		libp2p.ResourceManager(&network.NullResourceManager{}),
	)
	require.NoError(t, err)

	h2, err := libp2p.New(
		libp2p.NoListenAddrs,
		libp2p.EnableRelay(),
		libp2p.ResourceManager(&network.NullResourceManager{}),
	)
	require.NoError(t, err)

	relay1, err := libp2p.New()
	require.NoError(t, err)

	_, err = relay.New(relay1)
	require.NoError(t, err)

	relay1info := peer.AddrInfo{
		ID:    relay1.ID(),
		Addrs: relay1.Addrs(),
	}
	err = h1.Connect(context.Background(), relay1info)
	require.NoError(t, err)

	err = h2.Connect(context.Background(), relay1info)
	require.NoError(t, err)

	h2.SetStreamHandler("/testprotocol", func(s network.Stream) {
		fmt.Println("testprotocol")

		// End the example
		s.Close()
	})

	_, err = client.Reserve(context.Background(), h2, relay1info)
	require.NoError(t, err)

	relayaddr := ma.StringCast("/p2p/" + relay1info.ID.String() + "/p2p-circuit/p2p/" + h2.ID().String())

	h2Info := peer.AddrInfo{
		ID:    h2.ID(),
		Addrs: []ma.Multiaddr{relayaddr},
	}
	err = h1.Connect(context.Background(), h2Info)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx = network.WithNoDial(ctx, "test")
	_, err = h1.NewStream(ctx, h2.ID(), "/testprotocol")

	require.Error(t, err)

	_, err = h1.NewStream(network.WithUseTransient(context.Background(), "test"), h2.ID(), "/testprotocol")
	require.NoError(t, err)
}

func TestNewStreamTransientConnection(t *testing.T) {
	h1, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"),
		libp2p.EnableRelay(),
		libp2p.ResourceManager(&network.NullResourceManager{}),
	)
	require.NoError(t, err)

	h2, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"),
		libp2p.EnableRelay(),
		libp2p.ResourceManager(&network.NullResourceManager{}),
	)
	require.NoError(t, err)

	relay1, err := libp2p.New()
	require.NoError(t, err)

	_, err = relay.New(relay1)
	require.NoError(t, err)

	relay1info := peer.AddrInfo{
		ID:    relay1.ID(),
		Addrs: relay1.Addrs(),
	}
	err = h1.Connect(context.Background(), relay1info)
	require.NoError(t, err)

	err = h2.Connect(context.Background(), relay1info)
	require.NoError(t, err)

	h2.SetStreamHandler("/testprotocol", func(s network.Stream) {
		fmt.Println("testprotocol")

		// End the example
		s.Close()
	})

	_, err = client.Reserve(context.Background(), h2, relay1info)
	require.NoError(t, err)

	relayaddr := ma.StringCast("/p2p/" + relay1info.ID.String() + "/p2p-circuit/p2p/" + h2.ID().String())

	h1.Peerstore().AddAddr(h2.ID(), relayaddr, peerstore.TempAddrTTL)

	// NewStream should block transient connections till we have a direct connection
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	s, err := h1.NewStream(ctx, h2.ID(), "/testprotocol")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, s)

	// NewStream should return a stream if a direct connection is established
	// while waiting
	done := make(chan bool, 2)
	go func() {
		h1.Peerstore().AddAddrs(h2.ID(), h2.Addrs(), peerstore.TempAddrTTL)
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ctx = network.WithNoDial(ctx, "test")
		s, err = h1.NewStream(ctx, h2.ID(), "/testprotocol")
		require.NoError(t, err)
		require.NotNil(t, s)
		defer s.Close()
		require.Equal(t, network.DirInbound, s.Conn().Stat().Direction)
		done <- true
	}()
	go func() {
		// connect h2 to h1 simulating connection reversal
		h2.Peerstore().AddAddrs(h1.ID(), h1.Addrs(), peerstore.TempAddrTTL)
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		ctx = network.WithForceDirectDial(ctx, "test")
		err := h2.Connect(ctx, peer.AddrInfo{ID: h1.ID()})
		assert.NoError(t, err)
		done <- true
	}()
	<-done
	<-done
}

func TestWebRTCPrivateAddressAdvertisement(t *testing.T) {
	r, err := libp2p.New(
		// We need a public address for the relay
		libp2p.AddrsFactory(func(addrs []ma.Multiaddr) []ma.Multiaddr {
			return append(addrs, ma.StringCast("/ip4/1.2.3.4/udp/1/quic-v1/webtransport"))
		}),
		libp2p.EnableRelayService(),
		libp2p.ForceReachabilityPublic(),
	)
	require.NoError(t, err)

	relay1info := peer.AddrInfo{
		ID:    r.ID(),
		Addrs: r.Addrs(),
	}

	h, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"),
		libp2p.EnableRelay(),
		libp2p.EnableWebRTCPrivate(nil),
		libp2p.EnableAutoRelayWithStaticRelays(
			[]peer.AddrInfo{relay1info},
			autorelay.WithBootDelay(0),
		),
		libp2p.ForceReachabilityPrivate(),
	)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		for _, a := range h.Addrs() {
			_, rerr := a.ValueForProtocol(ma.P_CIRCUIT)
			_, werr := a.ValueForProtocol(ma.P_WEBRTC)
			if rerr == nil && werr == nil {
				return true
			}
		}
		return false
	}, 5*time.Second, 50*time.Millisecond)
}
