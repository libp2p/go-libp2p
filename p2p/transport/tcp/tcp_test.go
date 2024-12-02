package tcp

import (
	"context"
	"errors"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	mocknetwork "github.com/libp2p/go-libp2p/core/network/mocks"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/core/sec/insecure"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	tptu "github.com/libp2p/go-libp2p/p2p/net/upgrader"
	"github.com/libp2p/go-libp2p/p2p/transport/tcpreuse"
	ttransport "github.com/libp2p/go-libp2p/p2p/transport/testsuite"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

var muxers = []tptu.StreamMuxer{{ID: "/yamux", Muxer: yamux.DefaultTransport}}

func TestTcpTransport(t *testing.T) {
	for i := 0; i < 2; i++ {
		peerA, ia := makeInsecureMuxer(t)
		_, ib := makeInsecureMuxer(t)

		ua, err := tptu.New(ia, muxers, nil, nil, nil)
		require.NoError(t, err)
		ta, err := NewTCPTransport(ua, nil, nil)
		require.NoError(t, err)
		ub, err := tptu.New(ib, muxers, nil, nil, nil)
		require.NoError(t, err)
		tb, err := NewTCPTransport(ub, nil, nil)
		require.NoError(t, err)

		zero := "/ip4/127.0.0.1/tcp/0"
		ttransport.SubtestTransport(t, ta, tb, zero, peerA)

		tcpreuse.EnvReuseportVal = false
	}
	tcpreuse.EnvReuseportVal = true
}

func TestTcpTransportWithMetrics(t *testing.T) {
	peerA, ia := makeInsecureMuxer(t)
	_, ib := makeInsecureMuxer(t)

	ua, err := tptu.New(ia, muxers, nil, nil, nil)
	require.NoError(t, err)
	ta, err := NewTCPTransport(ua, nil, nil, WithMetrics())
	require.NoError(t, err)
	ub, err := tptu.New(ib, muxers, nil, nil, nil)
	require.NoError(t, err)
	tb, err := NewTCPTransport(ub, nil, nil, WithMetrics())
	require.NoError(t, err)

	zero := "/ip4/127.0.0.1/tcp/0"
	ttransport.SubtestTransport(t, ta, tb, zero, peerA)
}

func TestResourceManager(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	peerA, ia := makeInsecureMuxer(t)
	_, ib := makeInsecureMuxer(t)

	ua, err := tptu.New(ia, muxers, nil, nil, nil)
	require.NoError(t, err)
	ta, err := NewTCPTransport(ua, nil, nil)
	require.NoError(t, err)
	ln, err := ta.Listen(ma.StringCast("/ip4/127.0.0.1/tcp/0"))
	require.NoError(t, err)
	defer ln.Close()

	ub, err := tptu.New(ib, muxers, nil, nil, nil)
	require.NoError(t, err)
	rcmgr := mocknetwork.NewMockResourceManager(ctrl)
	tb, err := NewTCPTransport(ub, rcmgr, nil)
	require.NoError(t, err)

	t.Run("success", func(t *testing.T) {
		scope := mocknetwork.NewMockConnManagementScope(ctrl)
		rcmgr.EXPECT().OpenConnection(network.DirOutbound, true, ln.Multiaddr()).Return(scope, nil)
		scope.EXPECT().SetPeer(peerA)
		scope.EXPECT().PeerScope().Return(&network.NullScope{}).AnyTimes() // called by the upgrader
		conn, err := tb.Dial(context.Background(), ln.Multiaddr(), peerA)
		require.NoError(t, err)
		scope.EXPECT().Done()
		defer conn.Close()
	})

	t.Run("connection denied", func(t *testing.T) {
		rerr := errors.New("nope")
		rcmgr.EXPECT().OpenConnection(network.DirOutbound, true, ln.Multiaddr()).Return(nil, rerr)
		_, err = tb.Dial(context.Background(), ln.Multiaddr(), peerA)
		require.ErrorIs(t, err, rerr)
	})

	t.Run("peer denied", func(t *testing.T) {
		scope := mocknetwork.NewMockConnManagementScope(ctrl)
		rcmgr.EXPECT().OpenConnection(network.DirOutbound, true, ln.Multiaddr()).Return(scope, nil)
		rerr := errors.New("nope")
		scope.EXPECT().SetPeer(peerA).Return(rerr)
		scope.EXPECT().Done()
		_, err = tb.Dial(context.Background(), ln.Multiaddr(), peerA)
		require.ErrorIs(t, err, rerr)
	})
}

func TestTcpTransportCantDialDNS(t *testing.T) {
	for i := 0; i < 2; i++ {
		dnsa, err := ma.NewMultiaddr("/dns4/example.com/tcp/1234")
		require.NoError(t, err)

		var u transport.Upgrader
		tpt, err := NewTCPTransport(u, nil, nil)
		require.NoError(t, err)

		if tpt.CanDial(dnsa) {
			t.Fatal("shouldn't be able to dial dns")
		}

		tcpreuse.EnvReuseportVal = false
	}
	tcpreuse.EnvReuseportVal = true
}

func TestTcpTransportCantListenUtp(t *testing.T) {
	for i := 0; i < 2; i++ {
		utpa, err := ma.NewMultiaddr("/ip4/127.0.0.1/udp/0/utp")
		require.NoError(t, err)

		var u transport.Upgrader
		tpt, err := NewTCPTransport(u, nil, nil)
		require.NoError(t, err)

		_, err = tpt.Listen(utpa)
		require.Error(t, err, "shouldn't be able to listen on utp addr with tcp transport")

		tcpreuse.EnvReuseportVal = false
	}
	tcpreuse.EnvReuseportVal = true
}

func TestDialWithUpdates(t *testing.T) {
	peerA, ia := makeInsecureMuxer(t)
	_, ib := makeInsecureMuxer(t)

	ua, err := tptu.New(ia, muxers, nil, nil, nil)
	require.NoError(t, err)
	ta, err := NewTCPTransport(ua, nil, nil)
	require.NoError(t, err)
	ln, err := ta.Listen(ma.StringCast("/ip4/127.0.0.1/tcp/0"))
	require.NoError(t, err)
	defer ln.Close()

	ub, err := tptu.New(ib, muxers, nil, nil, nil)
	require.NoError(t, err)
	tb, err := NewTCPTransport(ub, nil, nil)
	require.NoError(t, err)

	updCh := make(chan transport.DialUpdate, 1)
	conn, err := tb.DialWithUpdates(context.Background(), ln.Multiaddr(), peerA, updCh)
	upd := <-updCh
	require.Equal(t, transport.UpdateKindHandshakeProgressed, upd.Kind)
	require.NotNil(t, conn)
	require.NoError(t, err)

	acceptAndClose := func() manet.Listener {
		li, err := manet.Listen(ma.StringCast("/ip4/127.0.0.1/tcp/0"))
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			conn, err := li.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}()
		return li
	}
	li := acceptAndClose()
	defer li.Close()
	// This dial will fail as acceptAndClose will not upgrade the connection
	conn, err = tb.DialWithUpdates(context.Background(), li.Multiaddr(), peerA, updCh)
	upd = <-updCh
	require.Equal(t, transport.UpdateKindHandshakeProgressed, upd.Kind)
	require.Nil(t, conn)
	require.Error(t, err)
}

func makeInsecureMuxer(t *testing.T) (peer.ID, []sec.SecureTransport) {
	t.Helper()
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 256)
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(priv)
	require.NoError(t, err)
	return id, []sec.SecureTransport{insecure.NewWithIdentity(insecure.ID, id, priv)}
}
