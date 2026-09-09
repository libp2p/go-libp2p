package quicreuse

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

func TestReuseportForNetwork(t *testing.T) {
	for _, tc := range []struct {
		name           string
		opts           []Option
		reuse4, reuse6 bool
	}{
		{name: "default", reuse4: true, reuse6: true},
		{name: "disabled", opts: []Option{DisableReuseport()}},
		{name: "udp4 disabled", opts: []Option{DisableReuseportForNetwork("udp4")}, reuse6: true},
		{name: "udp6 disabled", opts: []Option{DisableReuseportForNetwork("udp6")}, reuse4: true},
		{name: "both disabled", opts: []Option{DisableReuseportForNetwork("udp4"), DisableReuseportForNetwork("udp6")}},
		{name: "global then scoped", opts: []Option{DisableReuseport(), DisableReuseportForNetwork("udp6")}},
		{name: "scoped then global", opts: []Option{DisableReuseportForNetwork("udp6"), DisableReuseport()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, family := range []struct {
				network, listenAddr string
				remote              net.IP
				reuse               bool
			}{
				{"udp4", "/ip4/0.0.0.0/udp/0/quic-v1", net.IPv4(127, 0, 0, 1), tc.reuse4},
				{"udp6", "/ip6/::/udp/0/quic-v1", net.IPv6loopback, tc.reuse6},
			} {
				t.Run(family.network, func(t *testing.T) {
					if family.network == "udp6" {
						conn, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback})
						if err != nil {
							t.Skipf("IPv6 sockets unavailable: %v", err)
						}
						require.NoError(t, conn.Close())
					}
					for _, listening := range []bool{false, true} {
						t.Run(map[bool]string{false: "dial only", true: "listening"}[listening], func(t *testing.T) {
							cm, err := NewConnManager(quic.StatelessResetKey{}, quic.TokenGeneratorKey{}, tc.opts...)
							require.NoError(t, err)
							defer checkClosed(t, cm)
							defer cm.Close()
							var ln Listener
							if listening {
								_, tlsConf := getTLSConfForProto(t, "first")
								ln, err = cm.ListenQUICAndAssociate("test", ma.StringCast(family.listenAddr), tlsConf, nil)
								require.NoError(t, err)
								defer ln.Close()
								addr, err := ToQuicMultiaddr(ln.Addr(), quic.Version1)
								require.NoError(t, err)
								_, otherTLS := getTLSConfForProto(t, "second")
								other, err := cm.ListenQUIC(addr, otherTLS, nil)
								require.NoError(t, err)
								defer other.Close()
								require.Equal(t, ln.Addr(), other.Addr(), "different ALPNs still share the listener")
								shared, err := cm.SharedNonQUICPacketConn(family.network, ln.Addr().(*net.UDPAddr))
								if family.reuse {
									require.NoError(t, err)
									defer shared.Close()
								} else {
									require.ErrorContains(t, err, "socket reuse is disabled")
								}
							}
							remote := &net.UDPAddr{IP: family.remote, Port: 1234}
							first, err := cm.TransportWithAssociationForDial("test", family.network, remote)
							require.NoError(t, err)
							defer first.DecreaseCount()
							second, err := cm.TransportWithAssociationForDial("test", family.network, remote)
							require.NoError(t, err)
							defer second.DecreaseCount()
							if family.reuse {
								require.Same(t, first, second)
								if listening {
									require.Equal(t, ln.Addr(), first.LocalAddr())
								}
							} else {
								require.NotEqual(t, first.LocalAddr(), second.LocalAddr(), "both sockets are held open")
								if listening {
									require.NotEqual(t, ln.Addr(), first.LocalAddr())
								}
							}
						})
					}
				})
			}
		})
	}
}

func TestDisableReuseportForInvalidNetwork(t *testing.T) {
	for _, network := range []string{"", "udp", "tcp", "UDP6"} {
		cm, err := NewConnManager(quic.StatelessResetKey{}, quic.TokenGeneratorKey{}, DisableReuseportForNetwork(network))
		require.ErrorContains(t, err, "invalid reuseport network")
		require.Nil(t, cm)
	}
}

func TestDisabledReuseportReleasesFailedDial(t *testing.T) {
	var socket net.PacketConn
	cm, err := NewConnManager(quic.StatelessResetKey{}, quic.TokenGeneratorKey{},
		DisableReuseportForNetwork("udp4"),
		OverrideListenUDP(func(network string, addr *net.UDPAddr) (net.PacketConn, error) {
			conn, err := net.ListenUDP(network, addr)
			socket = conn
			return conn, err
		}),
	)
	require.NoError(t, err)
	defer cm.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = cm.DialQUIC(ctx, ma.StringCast("/ip4/127.0.0.1/udp/1234/quic-v1"), &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"test"}}, nil)
	require.Error(t, err)
	require.NotNil(t, socket)
	defer socket.Close()
	err = socket.SetReadDeadline(time.Now())
	require.ErrorIs(t, err, net.ErrClosed, "failed dials must close their dedicated UDP socket")
}

func TestLendTransportWithDisabledReuseport(t *testing.T) {
	cm, err := NewConnManager(quic.StatelessResetKey{}, quic.TokenGeneratorKey{}, DisableReuseportForNetwork("udp4"))
	require.NoError(t, err)
	defer cm.Close()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	require.NoError(t, err)
	defer conn.Close()
	done, err := cm.LendTransport("udp4", nil, conn)
	require.ErrorContains(t, err, "socket reuse is disabled")
	require.Nil(t, done)
}
