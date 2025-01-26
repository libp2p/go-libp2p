package libp2pfx

import (
	"context"
	"io"
	"testing"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
)

func TestConstructHost(t *testing.T) {
	app := fx.New(
		BlankHost(),
		SwarmNetwork(),
		RandomPeerID(),
		EventBus(),
		InMemoryPeerstore(),
		NullConnManager,
		MultistreamMuxer,
		fx.Invoke(func(h host.Host) {
			require.NotNil(t, h)
		}),
	)
	require.NoError(t, app.Start(context.Background()))
	defer assert.NoError(t, app.Stop(context.Background()))
}

func echoTest(t *testing.T, h1, h2 host.Host) {
	require.NoError(t,
		h1.Connect(context.Background(), peer.AddrInfo{
			ID:    h2.ID(),
			Addrs: h2.Addrs(),
		}))
	require.NoError(t,
		h2.Connect(context.Background(), peer.AddrInfo{
			ID:    h1.ID(),
			Addrs: h1.Addrs(),
		}))

	h2.SetStreamHandler("/test", func(s network.Stream) {
		defer s.Close()
		io.Copy(s, s)
	})

	s, err := h1.NewStream(context.Background(), h2.ID(), "/test")
	require.NoError(t, err)

	_, err = s.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, s.CloseWrite())

	b, err := io.ReadAll(s)
	require.NoError(t, err)
	require.Equal(t, "hello", string(b))
}

func TestQUICTransport(t *testing.T) {
	newHost := func() host.Host {
		var h host.Host
		app := fx.New(
			fx.NopLogger,
			BlankHost(),
			SwarmNetwork(),
			RandomPeerID(),
			EventBus(),
			InMemoryPeerstore(),
			MultistreamMuxer,
			QUICTransport,
			QUICReuseConnManager,
			NullConnectionGater,
			NullResourceManager,
			NullConnManager,
			fx.Supply(MetricsConfig{Disable: true}),
			fx.Populate(&h),
			ListenAddrs(multiaddr.StringCast("/ip4/127.0.0.1/udp/0/quic-v1")),
		)

		require.NoError(t, app.Start(context.Background()))
		t.Cleanup(func() { app.Stop(context.Background()) })
		return h
	}

	h1 := newHost()
	h2 := newHost()

	echoTest(t, h1, h2)
}

func TestTCPTransport(t *testing.T) {
	newHost := func() host.Host {
		var h host.Host
		app := fx.New(
			fx.NopLogger,
			BlankHost(),
			SwarmNetwork(),
			RandomPeerID(),
			EventBus(),
			InMemoryPeerstore(),
			MultistreamMuxer,
			QUICReuseConnManager,
			NullConnectionGater,
			NullResourceManager,
			NullConnManager,
			fx.Supply(MetricsConfig{Disable: true}),
			fx.Populate(&h),
			Upgrader(),
			TCPTransport(),
			Yamux,
			// TODO how to order?
			Security(
				TLS,
				Noise,
			),
			// Assert the security order is correct
			ListenAddrs(multiaddr.StringCast("/ip4/127.0.0.1/tcp/0")),
		)

		require.NoError(t, app.Start(context.Background()))
		t.Cleanup(func() { app.Stop(context.Background()) })
		return h
	}

	h1 := newHost()
	h2 := newHost()
	t.Log(h1.Addrs(), h2.Addrs())

	echoTest(t, h1, h2)
}

func TestSecurityOrder(t *testing.T) {
	app := fx.New(
		RandomPeerID(),
		Yamux,
		Security(
			TLS,
			Noise,
		),
		// Assert the security order is correct
		fx.Invoke(func(security []sec.SecureTransport) {
			ids := make([]string, len(security))
			for i, s := range security {
				ids[i] = string(s.ID())
			}
			assert.Equal(t, []string{"/tls/1.0.0", "/noise"}, ids)
		}),
	)
	require.NoError(t, app.Start(context.Background()))
	require.NoError(t, app.Stop(context.Background()))
}

func quicOptions() fx.Option {
	return fx.Options(
		fx.NopLogger,
		BlankHost(),
		SwarmNetwork(),
		RandomPeerID(),
		EventBus(),
		InMemoryPeerstore(),
		MultistreamMuxer,
		QUICTransport,
		QUICReuseConnManager,
		NullConnectionGater,
		NullResourceManager,
		NullConnManager,
		fx.Supply(MetricsConfig{Disable: true}),
		ListenAddrs(multiaddr.StringCast("/ip4/127.0.0.1/udp/0/quic-v1")),
	)
}

func TestPing(t *testing.T) {
	newHost := func() (host.Host, *ping.PingService) {
		var h host.Host
		var s *ping.PingService
		opts := quicOptions()
		app := fx.New(
			opts,
			PingService,
			fx.Populate(&h),
			fx.Populate(&s),
		)
		require.NoError(t, app.Start(context.Background()))
		t.Cleanup(func() { assert.NoError(t, app.Stop(context.Background())) })
		return h, s
	}

	h1, s := newHost()
	h2, _ := newHost()
	require.NoError(t, h1.Connect(context.Background(), peer.AddrInfo{h2.ID(), h2.Addrs()}))
	res := <-s.Ping(context.Background(), h2.ID())
	require.NoError(t, res.Error)
	t.Log(res.RTT)
}

func TestIdentify(t *testing.T) {
	newHost := func() (host.Host, identify.IDService, event.Bus) {
		var h host.Host
		var s identify.IDService
		var eb event.Bus
		opts := quicOptions()
		app := fx.New(
			opts,
			IdentifyService(),
			fx.Populate(&h, &s, &eb),
		)
		require.NoError(t, app.Start(context.Background()))
		t.Cleanup(func() { assert.NoError(t, app.Stop(context.Background())) })
		return h, s, eb
	}

	h1, s, eb1 := newHost()
	h2, _, eb2 := newHost()
	sub1, err := eb1.Subscribe(new(event.EvtPeerIdentificationCompleted))
	require.NoError(t, err)

	sub2, err := eb2.Subscribe(new(event.EvtPeerIdentificationCompleted))
	require.NoError(t, err)

	require.NoError(t, h1.Connect(context.Background(), peer.AddrInfo{h2.ID(), h2.Addrs()}))
	c := h1.Network().Conns()[0]
	<-s.IdentifyWait(c)
	res := (<-sub1.Out()).(event.EvtPeerIdentificationCompleted)
	t.Log(res)
	res = (<-sub2.Out()).(event.EvtPeerIdentificationCompleted)
	t.Log(res)
}
