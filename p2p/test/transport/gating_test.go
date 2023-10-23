package transport_integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/control"
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

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if strings.Contains(tc.Name, "WebRTCPrivate") {
				gomock.InOrder(
					connGater.EXPECT().InterceptPeerDial(h2.ID()).Return(true),
					connGater.EXPECT().InterceptAddrDial(h2.ID(), gomock.Any()).Return(true),

					// Calls for circuit-v2 conn setup
					connGater.EXPECT().InterceptPeerDial(h2.ID()).Return(true),
					connGater.EXPECT().InterceptAddrDial(h2.ID(), gomock.Any()).Return(true),
					connGater.EXPECT().InterceptAddrDial(gomock.Any(), gomock.Any()).Return(true), // two addresses for the peer

					// Calls for connection to relay node
					connGater.EXPECT().InterceptPeerDial(gomock.Any()).Return(true),
					connGater.EXPECT().InterceptAddrDial(gomock.Any(), gomock.Any()).Return(true),
					connGater.EXPECT().InterceptSecured(network.DirOutbound, gomock.Any(), gomock.Any()).Return(true),
					connGater.EXPECT().InterceptUpgraded(gomock.Any()).AnyTimes().Return(true, control.DisconnectReason(0)),

					// circuit-v2 setup complete
					connGater.EXPECT().InterceptSecured(network.DirOutbound, gomock.Any(), gomock.Any()).Return(true),
					connGater.EXPECT().InterceptUpgraded(gomock.Any()).AnyTimes().Return(true, control.DisconnectReason(0)),

					// webrtcprivate setup complete
					connGater.EXPECT().InterceptSecured(network.DirOutbound, h2.ID(), gomock.Any()).Do(func(_, _ any, c network.ConnMultiaddrs) {
						require.Equal(t, h2.Addrs()[0], c.RemoteMultiaddr())
					}),
				)
				// As we are using trickle ICE, some candidates are handled
				// as prflx candidates. The default wait time for prflx
				// candidates is 1 second. Bump the timeout to handle those
				// cases.
				ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				// force a direct connection
				ctx = network.WithForceDirectDial(ctx, "integration test /webrtc")
			} else {
				gomock.InOrder(
					connGater.EXPECT().InterceptPeerDial(h2.ID()).Return(true),
					connGater.EXPECT().InterceptAddrDial(h2.ID(), gomock.Any()).Return(true),
					connGater.EXPECT().InterceptSecured(network.DirOutbound, h2.ID(), gomock.Any()).Do(func(_ network.Direction, _ peer.ID, addrs network.ConnMultiaddrs) {
						// remove the certhash component from WebTransport and WebRTC addresses
						require.Equal(t, stripCertHash(h2.Addrs()[0]).String(), addrs.RemoteMultiaddr().String())
					}),
				)
			}

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
			if strings.Contains(tc.Name, "WebRTCPrivate") {
				gomock.InOrder(
					connGater.EXPECT().InterceptPeerDial(h2.ID()).Return(true),
					connGater.EXPECT().InterceptAddrDial(h2.ID(), gomock.Any()).Return(true),

					// Calls for circuit-v2 conn setup
					connGater.EXPECT().InterceptPeerDial(h2.ID()).Return(true),
					connGater.EXPECT().InterceptAddrDial(h2.ID(), gomock.Any()).Return(true),
					connGater.EXPECT().InterceptAddrDial(gomock.Any(), gomock.Any()).Return(true), // two addresses for the peer

					// Calls for connection to relay node
					connGater.EXPECT().InterceptPeerDial(gomock.Any()).Return(true),
					connGater.EXPECT().InterceptAddrDial(gomock.Any(), gomock.Any()).Return(true),
					connGater.EXPECT().InterceptSecured(network.DirOutbound, gomock.Any(), gomock.Any()).Return(true),
					connGater.EXPECT().InterceptUpgraded(gomock.Any()).Return(true, control.DisconnectReason(0)),

					// circuit-v2 setup complete
					connGater.EXPECT().InterceptSecured(network.DirOutbound, gomock.Any(), gomock.Any()).Return(true),
					connGater.EXPECT().InterceptUpgraded(gomock.Any()).Return(true, control.DisconnectReason(0)),

					// webrtcprivate setup complete
					connGater.EXPECT().InterceptSecured(network.DirOutbound, h2.ID(), gomock.Any()).Return(true),
					connGater.EXPECT().InterceptUpgraded(gomock.Any()).Do(func(c network.Conn) {
						require.Equal(t, h1.ID(), c.LocalPeer())
						require.Equal(t, h2.ID(), c.RemotePeer())
						require.Equal(t, h2.Addrs()[0], c.RemoteMultiaddr())
					}),
				)
				// As we are using trickle ICE, some candidates are handled
				// as prflx candidates. The default wait time for prflx
				// candidates is 1 second. Bump the timeout to handle those
				// cases.
				ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				// force a direct connection
				ctx = network.WithForceDirectDial(ctx, "integration test /webrtc")
			} else {
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
			}
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
			if strings.Contains(tc.Name, "WebRTCPrivate") {
				testInterceptAcceptIncomingWebRTCPrivate(t, tc)
				return
			}
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
			} else {
				connGater.EXPECT().InterceptAccept(gomock.Any()).Do(func(addrs network.ConnMultiaddrs) {
					// remove the certhash component from WebTransport addresses
					require.Equal(t, stripCertHash(h2.Addrs()[0]), addrs.LocalMultiaddr())
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

func testInterceptAcceptIncomingWebRTCPrivate(t *testing.T, tc TransportTestCase) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	connGater := NewMockConnectionGater(ctrl)

	// Relay reservation calls
	connGater.EXPECT().InterceptPeerDial(gomock.Any()).Return(true)
	connGater.EXPECT().InterceptAddrDial(gomock.Any(), gomock.Any()).Return(true)
	connGater.EXPECT().InterceptSecured(gomock.Any(), gomock.Any(), gomock.Any()).Return(true)
	connGater.EXPECT().InterceptUpgraded(gomock.Any()).Return(true, control.DisconnectReason(0))

	h1 := tc.HostGenerator(t, TransportTestCaseOpts{NoListen: true})
	h2 := tc.HostGenerator(t, TransportTestCaseOpts{ConnGater: connGater})
	defer h1.Close()
	defer h2.Close()
	require.Len(t, h2.Addrs(), 1)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// relayed connection for incoming stream
	connGater.EXPECT().InterceptAccept(gomock.Any()).Return(true)
	connGater.EXPECT().InterceptSecured(gomock.Any(), gomock.Any(), gomock.Any()).Return(true)
	connGater.EXPECT().InterceptUpgraded(gomock.Any()).Return(true, control.DisconnectReason(0))
	// webrtc connection accept
	connGater.EXPECT().InterceptAccept(gomock.Any()).Do(func(c network.ConnMultiaddrs) {
		// On an incoming webrtc connection, the remote address will be the same as the listen address.
		// This is simlar to how addresses are setup on circuit-v2 connections
		require.Equal(t, h2.Addrs()[0], c.RemoteMultiaddr(), h2.Addrs(), c.RemoteMultiaddr())
	})

	// The basic host dials the first connection.
	h1.Peerstore().AddAddrs(h2.ID(), h2.Addrs(), time.Hour)
	_, err := h1.NewStream(ctx, h2.ID(), protocol.TestingID)
	require.Error(t, err)
	// Do not check that error is Context Deadline Exceeded
	// See details at the end of `testInterceptSecuredIncomingWebRTCPrivate`
}

func TestInterceptSecuredIncoming(t *testing.T) {
	if race.WithRace() {
		t.Skip("The upgrader spawns a new Go routine, which leads to race conditions when using GoMock.")
	}
	for _, tc := range transportsToTest {
		t.Run(tc.Name, func(t *testing.T) {
			if strings.Contains(tc.Name, "WebRTCPrivate") {
				testInterceptSecuredIncomingWebRTCPrivate(t, tc)
				return
			}

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

func testInterceptSecuredIncomingWebRTCPrivate(t *testing.T, tc TransportTestCase) {
	t.Helper()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	connGater := NewMockConnectionGater(ctrl)
	gomock.InOrder(
		// Dial to relay node for circuit reservation
		connGater.EXPECT().InterceptPeerDial(gomock.Any()).Return(true),
		connGater.EXPECT().InterceptAddrDial(gomock.Any(), gomock.Any()).Return(true),
		connGater.EXPECT().InterceptSecured(gomock.Any(), gomock.Any(), gomock.Any()).Return(true),
		connGater.EXPECT().InterceptUpgraded(gomock.Any()).Return(true, control.DisconnectReason(0)),
		// Incoming relay connection for signaling stream
		connGater.EXPECT().InterceptAccept(gomock.Any()).Return(true),
		connGater.EXPECT().InterceptSecured(gomock.Any(), gomock.Any(), gomock.Any()).Return(true),
		connGater.EXPECT().InterceptUpgraded(gomock.Any()).Return(true, control.DisconnectReason(0)),
	)
	h1 := tc.HostGenerator(t, TransportTestCaseOpts{NoListen: true})
	h2 := tc.HostGenerator(t, TransportTestCaseOpts{ConnGater: connGater})
	defer h1.Close()
	defer h2.Close()

	require.Len(t, h2.Addrs(), 1)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = network.WithForceDirectDial(ctx, "transport integration test /webrtc")
	gomock.InOrder(
		connGater.EXPECT().InterceptAccept(gomock.Any()).Return(true),
		connGater.EXPECT().InterceptSecured(network.DirInbound, h1.ID(), gomock.Any()).Do(func(_, _ any, c network.ConnMultiaddrs) {
			require.Equal(t, h2.Addrs()[0], c.RemoteMultiaddr())
		}),
	)

	h1.Peerstore().AddAddrs(h2.ID(), h2.Addrs(), time.Hour)
	_, err := h1.NewStream(ctx, h2.ID(), protocol.TestingID)
	require.Error(t, err)

	// Do not check that error is Context Deadline Exceeded
	// WebRTCPrivate connection establishment is considered complete when the DTLS handshake finishes.
	// At this point no SCTP association is established. Closing the connection on the listener side,
	// immediately after accepting, closes the listener side of the connection before the SCTP association
	// is established. Pion doesn't handle this case nicely and webrtc.PeerConnection on the dialer will
	// only be considered closed once the ICE transport times out.
}

func TestInterceptUpgradedIncoming(t *testing.T) {
	if race.WithRace() {
		t.Skip("The upgrader spawns a new Go routine, which leads to race conditions when using GoMock.")
	}
	for _, tc := range transportsToTest {
		t.Run(tc.Name, func(t *testing.T) {
			if strings.Contains(tc.Name, "WebRTCPrivate") {
				testInterceptUpgradeIncomingWebRTCPrivate(t, tc)
				return
			}
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			connGater := NewMockConnectionGater(ctrl)

			h1 := tc.HostGenerator(t, TransportTestCaseOpts{NoListen: true})
			h2 := tc.HostGenerator(t, TransportTestCaseOpts{ConnGater: connGater})
			defer h1.Close()
			defer h2.Close()
			require.Len(t, h2.Addrs(), 1)

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
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			h1.Peerstore().AddAddrs(h2.ID(), h2.Addrs(), time.Hour)
			_, err := h1.NewStream(ctx, h2.ID(), protocol.TestingID)
			require.Error(t, err)
			require.NotErrorIs(t, err, context.DeadlineExceeded)
		})
	}
}

func testInterceptUpgradeIncomingWebRTCPrivate(t *testing.T, tc TransportTestCase) {
	t.Helper()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	connGater := NewMockConnectionGater(ctrl)

	gomock.InOrder(
		// Dial to relay node for circuit reservation
		connGater.EXPECT().InterceptPeerDial(gomock.Any()).Return(true),
		connGater.EXPECT().InterceptAddrDial(gomock.Any(), gomock.Any()).Return(true),
		connGater.EXPECT().InterceptSecured(gomock.Any(), gomock.Any(), gomock.Any()).Return(true),
		connGater.EXPECT().InterceptUpgraded(gomock.Any()).Return(true, control.DisconnectReason(0)),
		// Incoming relay connection for signaling stream
		connGater.EXPECT().InterceptAccept(gomock.Any()).Return(true),
		connGater.EXPECT().InterceptSecured(gomock.Any(), gomock.Any(), gomock.Any()).Return(true),
		connGater.EXPECT().InterceptUpgraded(gomock.Any()).Return(true, control.DisconnectReason(0)),
	)

	h1 := tc.HostGenerator(t, TransportTestCaseOpts{NoListen: true})
	h2 := tc.HostGenerator(t, TransportTestCaseOpts{ConnGater: connGater})
	defer h1.Close()
	defer h2.Close()

	require.Len(t, h2.Addrs(), 1)

	gomock.InOrder(
		connGater.EXPECT().InterceptAccept(gomock.Any()).Return(true),
		connGater.EXPECT().InterceptSecured(network.DirInbound, h1.ID(), gomock.Any()).Return(true),
		connGater.EXPECT().InterceptUpgraded(gomock.Any()).Do(func(c network.Conn) {
			require.Equal(t, h2.Addrs()[0], c.RemoteMultiaddr())
			require.Equal(t, h1.ID(), c.RemotePeer())
			require.Equal(t, h2.ID(), c.LocalPeer())
		}),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = network.WithForceDirectDial(ctx, "transport integration test /webrtc")

	h1.Peerstore().AddAddrs(h2.ID(), h2.Addrs(), time.Hour)
	_, err := h1.NewStream(ctx, h2.ID(), protocol.TestingID)
	require.Error(t, err)
	// Do not check that error is Context Deadline Exceeded
	// See details at the end of `testInterceptSecuredIncomingWebRTCPrivate`
}
