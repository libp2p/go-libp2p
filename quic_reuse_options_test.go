package libp2p

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
)

func TestQUICReuseOptionsRejectCustomConstructor(t *testing.T) {
	custom := QUICReuse(quicreuse.NewConnManager)
	opts := QUICReuseOptions(quicreuse.DisableReuseportForNetwork("udp6"))
	for _, options := range [][]Option{{custom, opts}, {opts, custom}} {
		h, err := New(options...)
		require.ErrorContains(t, err, "QUICReuseOptions cannot be combined")
		require.Nil(t, h)
	}
}

func TestQUICReuseOptionsRejectInvalidNetwork(t *testing.T) {
	h, err := New(QUICReuseOptions(quicreuse.DisableReuseportForNetwork("tcp")))
	require.ErrorContains(t, err, "invalid reuseport network")
	require.Nil(t, h)
}

func TestQUICReuseOptionsPreserveManagerShutdown(t *testing.T) {
	var cm *quicreuse.ConnManager
	h, err := New(NoListenAddrs,
		QUICReuseOptions(quicreuse.DisableReuseportForNetwork("udp6")),
		WithFxOption(fx.Populate(&cm)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { h.Close() })
	tr, err := cm.TransportForDial("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234})
	require.NoError(t, err)
	addr := tr.LocalAddr().(*net.UDPAddr)
	tr.DecreaseCount()
	require.NoError(t, h.Close())
	conn, err := net.ListenUDP("udp4", addr)
	require.NoError(t, err, "host shutdown must close idle sockets in the manager's reuse pool")
	conn.Close()
}

type quicResourceManager struct {
	network.NullResourceManager
	opened, verified, released atomic.Int32
}

func (r *quicResourceManager) OpenConnection(dir network.Direction, _ bool, _ ma.Multiaddr) (network.ConnManagementScope, error) {
	if dir != network.DirInbound {
		return &network.NullScope{}, nil
	}
	r.opened.Add(1)
	return &quicResourceScope{released: &r.released}, nil
}

func (r *quicResourceManager) VerifySourceAddress(net.Addr) bool {
	r.verified.Add(1)
	return true
}

type quicResourceScope struct {
	network.NullScope
	released *atomic.Int32
	once     sync.Once
}

func (s *quicResourceScope) Done() {
	s.once.Do(func() { s.released.Add(1) })
}

func TestQUICReuseOptionsPreserveResourceManager(t *testing.T) {
	for _, listenAddr := range []string{"/ip4/127.0.0.1/udp/0/quic-v1", "/ip6/::1/udp/0/quic-v1"} {
		t.Run(listenAddr, func(t *testing.T) {
			if listenAddr == "/ip6/::1/udp/0/quic-v1" {
				probe, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback})
				if err != nil {
					t.Skipf("IPv6 sockets unavailable: %v", err)
				}
				probe.Close()
			}
			rm := &quicResourceManager{}
			registry := prometheus.NewRegistry()
			server, err := New(ListenAddrStrings(listenAddr), ResourceManager(rm),
				PrometheusRegisterer(registry),
				QUICReuseOptions(quicreuse.DisableReuseportForNetwork("udp6")),
			)
			require.NoError(t, err)
			t.Cleanup(func() { server.Close() })
			client, err := New(NoListenAddrs)
			require.NoError(t, err)
			t.Cleanup(func() { client.Close() })
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			require.NoError(t, client.Connect(ctx, peer.AddrInfo{ID: server.ID(), Addrs: server.Addrs()}))
			require.Positive(t, rm.opened.Load(), "inbound resource accounting is installed")
			require.Positive(t, rm.verified.Load(), "resource-manager source verification is installed")
			metrics, err := registry.Gather()
			require.NoError(t, err)
			require.NotEmpty(t, metrics, "the configured metrics registry is retained")
			require.NoError(t, client.Close())
			require.Eventually(t, func() bool { return rm.released.Load() == rm.opened.Load() }, time.Second, 10*time.Millisecond)
		})
	}
}
