package transport_integration

import (
	"context"
	"encoding/binary"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"

	"github.com/libp2p/go-libp2p-testing/race"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

//go:generate go run go.uber.org/mock/mockgen -package transport_integration -destination mock_connection_gater_test.go github.com/libp2p/go-libp2p/core/connmgr ConnectionGater

func stripCertHash(addr ma.Multiaddr) ma.Multiaddr {
	for {
		if _, err := addr.ValueForProtocol(ma.P_CERTHASH); err != nil {
			break
		}
		addr, _ = ma.SplitLast(addr)
	}
	return addr
}

func addrPort(addr ma.Multiaddr) netip.AddrPort {
	a := netip.Addr{}
	p := uint16(0)
	ma.ForEach(addr, func(c ma.Component) bool {
		if c.Protocol().Code == ma.P_IP4 || c.Protocol().Code == ma.P_IP6 {
			a, _ = netip.AddrFromSlice(c.RawValue())
			return false
		}
		if c.Protocol().Code == ma.P_UDP || c.Protocol().Code == ma.P_TCP {
			p = binary.BigEndian.Uint16(c.RawValue())
			return true
		}
		return false
	})
	return netip.AddrPortFrom(a, p)
}

func TestInterceptPeerDial(t *testing.T) {
	if race.WithRace() {
		t.Skip("The upgrader spawns a new Go routine, which leads to race conditions when using GoMock.")
	}
	for _, tc := range transportsToTest {
		t.Run(tc.Name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			connGater := NewMockConnectionGater(ctrl)

			h1 := tc.HostGenerator(t, TransportTestCaseOpts{NoListen: true, ConnGater: connGater})
			h2 := tc.HostGenerator(t, TransportTestCaseOpts{})
			require.Len(t, h2.Addrs(), 1)

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			connGater.EXPECT().InterceptPeerDial(h2.ID())
			require.ErrorIs(t, h1.Connect(ctx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()}), swarm.ErrGaterDisallowedConnection)
		})
	}
}

func TestInterceptAddrDial(t *testing.T) {
	if race.WithRace() {
		t.Skip("The upgrader spawns a new Go routine, which leads to race conditions when using GoMock.")
	}
	for _, tc := range transportsToTest {
		t.Run(tc.Name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			connGater := NewMockConnectionGater(ctrl)

			h1 := tc.HostGenerator(t, TransportTestCaseOpts{NoListen: true, ConnGater: connGater})
			h2 := tc.HostGenerator(t, TransportTestCaseOpts{})
			require.Len(t, h2.Addrs(), 1)

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			gomock.InOrder(
				connGater.EXPECT().InterceptPeerDial(h2.ID()).Return(true),
				connGater.EXPECT().InterceptAddrDial(h2.ID(), h2.Addrs()[0]),
			)
			require.ErrorIs(t, h1.Connect(ctx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()}), swarm.ErrNoGoodAddresses)
		})
	}
}

func TestInterceptSecuredOutgoing(t *testing.T) {
	if race.WithRace() {
		t.Skip("The upgrader spawns a new Go routine, which leads to race conditions when using GoMock.")
	}
	for _, tc := range transportsToTest {
		t.Run(tc.Name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			connGater := NewMockConnectionGater(ctrl)

			h1 := tc.HostGenerator(t, TransportTestCaseOpts{NoListen: true, ConnGater: connGater})
			h2 := tc.HostGenerator(t, TransportTestCaseOpts{})
			defer h1.Close()
			defer h2.Close()
			require.Len(t, h2.Addrs(), 1)
			require.Len(t, h2.Addrs(), 1)

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			gomock.InOrder(
				connGater.EXPECT().InterceptPeerDial(h2.ID()).Return(true),
				connGater.EXPECT().InterceptAddrDial(h2.ID(), gomock.Any()).Return(true),
				connGater.EXPECT().InterceptSecured(network.DirOutbound, h2.ID(), gomock.Any()).Do(func(_ network.Direction, _ peer.ID, addrs network.ConnMultiaddrs) {
					// remove the certhash component from WebTransport and WebRTC addresses
					require.Equal(t, stripCertHash(h2.Addrs()[0]).String(), addrs.RemoteMultiaddr().String())
				}),
			)
			err := h1.Connect(ctx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()})
			require.Error(t, err)
			require.NotErrorIs(t, err, context.DeadlineExceeded)
		})
	}
}

func TestInterceptUpgradedOutgoing(t *testing.T) {
	if race.WithRace() {
		t.Skip("The upgrader spawns a new Go routine, which leads to race conditions when using GoMock.")
	}
	for _, tc := range transportsToTest {
		t.Run(tc.Name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			connGater := NewMockConnectionGater(ctrl)

			h1 := tc.HostGenerator(t, TransportTestCaseOpts{NoListen: true, ConnGater: connGater})
			h2 := tc.HostGenerator(t, TransportTestCaseOpts{})
			defer h1.Close()
			defer h2.Close()
			require.Len(t, h2.Addrs(), 1)
			require.Len(t, h2.Addrs(), 1)

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			gomock.InOrder(
				connGater.EXPECT().InterceptPeerDial(h2.ID()).Return(true),
				connGater.EXPECT().InterceptAddrDial(h2.ID(), gomock.Any()).Return(true),
				connGater.EXPECT().InterceptSecured(network.DirOutbound, h2.ID(), gomock.Any()).Return(true),
				connGater.EXPECT().InterceptUpgraded(gomock.Any()).Do(func(c network.Conn) {
					// remove the certhash component from WebTransport addresses
					require.Equal(t, stripCertHash(h2.Addrs()[0]), c.RemoteMultiaddr())
					require.Equal(t, h1.ID(), c.LocalPeer())
					require.Equal(t, h2.ID(), c.RemotePeer())
				}))
			err := h1.Connect(ctx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()})
			require.Error(t, err)
			require.NotErrorIs(t, err, context.DeadlineExceeded)
		})
	}
}

func TestInterceptAccept(t *testing.T) {
	if race.WithRace() {
		t.Skip("The upgrader spawns a new Go routine, which leads to race conditions when using GoMock.")
	}
	for _, tc := range transportsToTest {
		t.Run(tc.Name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			connGater := NewMockConnectionGater(ctrl)

			h1 := tc.HostGenerator(t, TransportTestCaseOpts{NoListen: true})
			h2 := tc.HostGenerator(t, TransportTestCaseOpts{ConnGater: connGater})
			defer h1.Close()
			defer h2.Close()
			require.Len(t, h2.Addrs(), 1)

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			// The basic host dials the first connection.
			if strings.Contains(tc.Name, "WebRTC") {
				// In WebRTC, retransmissions of the STUN packet might cause us to create multiple connections,
				// if the first connection attempt is rejected.
				connGater.EXPECT().InterceptAccept(gomock.Any()).Do(func(addrs network.ConnMultiaddrs) {
					// remove the certhash component from WebTransport addresses
					require.Equal(t, stripCertHash(h2.Addrs()[0]), addrs.LocalMultiaddr())
				}).AnyTimes()
			} else if strings.Contains(tc.Name, "WebSocket-Shared") {
				connGater.EXPECT().InterceptAccept(gomock.Any()).Do(func(addrs network.ConnMultiaddrs) {
					require.Equal(t, addrPort(h2.Addrs()[0]), addrPort(addrs.LocalMultiaddr()))
				})
			} else {
				connGater.EXPECT().InterceptAccept(gomock.Any()).Do(func(addrs network.ConnMultiaddrs) {
					// remove the certhash component from WebTransport addresses
					require.Equal(t, stripCertHash(h2.Addrs()[0]), addrs.LocalMultiaddr(), "%s\n%s", h2.Addrs()[0], addrs.LocalMultiaddr())
				})
			}

			h1.Peerstore().AddAddrs(h2.ID(), h2.Addrs(), time.Hour)
			_, err := h1.NewStream(ctx, h2.ID(), protocol.TestingID)
			require.Error(t, err)
			if _, err := h2.Addrs()[0].ValueForProtocol(ma.P_WEBRTC_DIRECT); err != nil {
				// WebRTC rejects connection attempt before an error can be sent to the client.
				// This means that the connection attempt will time out.
				require.NotErrorIs(t, err, context.DeadlineExceeded)
			}
		})
	}
}

func TestInterceptSecuredIncoming(t *testing.T) {
	if race.WithRace() {
		t.Skip("The upgrader spawns a new Go routine, which leads to race conditions when using GoMock.")
	}
	for _, tc := range transportsToTest {
		t.Run(tc.Name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			connGater := NewMockConnectionGater(ctrl)

			h1 := tc.HostGenerator(t, TransportTestCaseOpts{NoListen: true})
			h2 := tc.HostGenerator(t, TransportTestCaseOpts{ConnGater: connGater})
			defer h1.Close()
			defer h2.Close()
			require.Len(t, h2.Addrs(), 1)

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			gomock.InOrder(
				connGater.EXPECT().InterceptAccept(gomock.Any()).Return(true),
				connGater.EXPECT().InterceptSecured(network.DirInbound, h1.ID(), gomock.Any()).Do(func(_ network.Direction, _ peer.ID, addrs network.ConnMultiaddrs) {
					// remove the certhash component from WebTransport addresses
					require.Equal(t, stripCertHash(h2.Addrs()[0]), addrs.LocalMultiaddr())
				}),
			)
			h1.Peerstore().AddAddrs(h2.ID(), h2.Addrs(), time.Hour)
			_, err := h1.NewStream(ctx, h2.ID(), protocol.TestingID)
			require.Error(t, err)
			require.NotErrorIs(t, err, context.DeadlineExceeded)
		})
	}
}

func TestInterceptUpgradedIncoming(t *testing.T) {
	if race.WithRace() {
		t.Skip("The upgrader spawns a new Go routine, which leads to race conditions when using GoMock.")
	}
	for _, tc := range transportsToTest {
		t.Run(tc.Name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			connGater := NewMockConnectionGater(ctrl)

			h1 := tc.HostGenerator(t, TransportTestCaseOpts{NoListen: true})
			h2 := tc.HostGenerator(t, TransportTestCaseOpts{ConnGater: connGater})
			defer h1.Close()
			defer h2.Close()
			require.Len(t, h2.Addrs(), 1)

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			gomock.InOrder(
				connGater.EXPECT().InterceptAccept(gomock.Any()).Return(true),
				connGater.EXPECT().InterceptSecured(network.DirInbound, h1.ID(), gomock.Any()).Return(true),
				connGater.EXPECT().InterceptUpgraded(gomock.Any()).Do(func(c network.Conn) {
					// remove the certhash component from WebTransport addresses
					require.Equal(t, stripCertHash(h2.Addrs()[0]), c.LocalMultiaddr())
					require.Equal(t, h1.ID(), c.RemotePeer())
					require.Equal(t, h2.ID(), c.LocalPeer())
				}),
			)
			h1.Peerstore().AddAddrs(h2.ID(), h2.Addrs(), time.Hour)
			_, err := h1.NewStream(ctx, h2.ID(), protocol.TestingID)
			require.Error(t, err)
			require.NotErrorIs(t, err, context.DeadlineExceeded)
		})
	}
}
