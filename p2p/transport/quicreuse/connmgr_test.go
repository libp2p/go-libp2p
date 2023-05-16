package quicreuse

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/transport"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

func checkClosed(t *testing.T, cm *ConnManager) {
	for _, r := range []*reuse{cm.reuseUDP4, cm.reuseUDP6} {
		if r == nil {
			continue
		}
		r.mutex.Lock()
		for _, conn := range r.globalListeners {
			require.Zero(t, conn.GetCount())
		}
		for _, conns := range r.unicast {
			for _, conn := range conns {
				require.Zero(t, conn.GetCount())
			}
		}
		r.mutex.Unlock()
	}
	require.Eventually(t, func() bool { return !isGarbageCollectorRunning() }, 200*time.Millisecond, 10*time.Millisecond)
}

func TestListenQUICDraft29Disabled(t *testing.T) {
	cm, err := NewConnManager([32]byte{}, DisableDraft29(), DisableReuseport())
	require.NoError(t, err)
	defer cm.Close()
	_, err = cm.ListenQUIC(ma.StringCast("/ip4/127.0.0.1/udp/0/quic"), &tls.Config{}, nil)
	require.EqualError(t, err, "can't listen on `/quic` multiaddr (QUIC draft 29 version) when draft 29 support is disabled")
	ln, err := cm.ListenQUIC(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1"), &tls.Config{NextProtos: []string{"proto"}}, nil)
	require.NoError(t, err)
	require.NoError(t, ln.Close())
	require.False(t, isGarbageCollectorRunning())
}

func TestListenOnSameProto(t *testing.T) {
	t.Run("with reuseport", func(t *testing.T) {
		testListenOnSameProto(t, true)
	})

	t.Run("without reuseport", func(t *testing.T) {
		testListenOnSameProto(t, false)
	})
}

func testListenOnSameProto(t *testing.T, enableReuseport bool) {
	var opts []Option
	if !enableReuseport {
		opts = append(opts, DisableReuseport())
	}
	cm, err := NewConnManager([32]byte{}, opts...)
	require.NoError(t, err)
	defer checkClosed(t, cm)
	defer cm.Close()

	const alpn = "proto"

	var tlsConf tls.Config
	tlsConf.NextProtos = []string{alpn}
	ln1, err := cm.ListenQUIC(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1"), &tls.Config{NextProtos: []string{alpn}}, nil)
	require.NoError(t, err)
	defer ln1.Close()

	addr := ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/udp/%d/quic-v1", ln1.Addr().(*net.UDPAddr).Port))
	_, err = cm.ListenQUIC(addr, &tls.Config{NextProtos: []string{alpn}}, nil)
	require.EqualError(t, err, "already listening for protocol "+alpn)

	// listening on a different address works
	ln2, err := cm.ListenQUIC(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1"), &tls.Config{NextProtos: []string{alpn}}, nil)
	require.NoError(t, err)
	defer ln2.Close()
}

// The conn passed to quic-go should be a conn that quic-go can be
// type-asserted to a UDPConn. That way, it can use all kinds of optimizations.
func TestConnectionPassedToQUICForListening(t *testing.T) {
	origQuicListen := quicListen
	t.Cleanup(func() { quicListen = origQuicListen })

	var conn net.PacketConn
	quicListen = func(c net.PacketConn, _ *tls.Config, _ *quic.Config) (quic.Listener, error) {
		conn = c
		return nil, errors.New("listen error")
	}

	cm, err := NewConnManager([32]byte{}, DisableReuseport())
	require.NoError(t, err)
	defer cm.Close()

	_, err = cm.ListenQUIC(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1"), &tls.Config{NextProtos: []string{"proto"}}, nil)
	require.EqualError(t, err, "listen error")
	require.NotNil(t, conn)
	defer conn.Close()
	if _, ok := conn.(quic.OOBCapablePacketConn); !ok {
		t.Fatal("connection passed to quic-go cannot be type asserted to a *net.UDPConn")
	}
}

type mockFailAcceptListener struct {
	addr net.Addr
}

// Accept implements quic.Listener
func (l *mockFailAcceptListener) Accept(context.Context) (quic.Connection, error) {
	return nil, fmt.Errorf("Some error")
}

// Addr implements quic.Listener
func (l *mockFailAcceptListener) Addr() net.Addr {
	return l.addr
}

// Close implements quic.Listener
func (l *mockFailAcceptListener) Close() error {
	return nil
}

var _ quic.Listener = &mockFailAcceptListener{}

func TestAcceptErrorGetCleanedUp(t *testing.T) {
	origQuicListen := quicListen
	t.Cleanup(func() { quicListen = origQuicListen })

	quicListen = func(c net.PacketConn, _ *tls.Config, _ *quic.Config) (quic.Listener, error) {
		return &mockFailAcceptListener{
			addr: c.LocalAddr(),
		}, nil
	}

	cm, err := NewConnManager([32]byte{}, DisableReuseport())
	require.NoError(t, err)
	defer cm.Close()

	l, err := cm.ListenQUIC(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1"), &tls.Config{NextProtos: []string{"proto"}}, nil)
	require.NoError(t, err)
	defer l.Close()
	_, err = l.Accept(context.Background())
	require.ErrorIs(t, err, transport.ErrListenerClosed)
}

// The connection passed to quic-go needs to be type-assertable to a net.UDPConn,
// in order to enable features like batch processing and ECN.
func TestConnectionPassedToQUICForDialing(t *testing.T) {
	origQuicDialContext := quicDialContext
	defer func() { quicDialContext = origQuicDialContext }()

	var conn net.PacketConn
	quicDialContext = func(_ context.Context, c net.PacketConn, _ net.Addr, _ string, _ *tls.Config, _ *quic.Config) (quic.Connection, error) {
		conn = c
		return nil, errors.New("dial error")
	}

	cm, err := NewConnManager([32]byte{}, DisableReuseport())
	require.NoError(t, err)
	defer cm.Close()

	_, err = cm.DialQUIC(context.Background(), ma.StringCast("/ip4/127.0.0.1/udp/1234/quic-v1"), &tls.Config{}, nil)
	require.EqualError(t, err, "dial error")
	require.NotNil(t, conn)
	defer conn.Close()
	if _, ok := conn.(quic.OOBCapablePacketConn); !ok {
		t.Fatal("connection passed to quic-go cannot be type asserted to a *net.UDPConn")
	}
}

func getTLSConfForProto(t *testing.T, alpn string) (peer.ID, *tls.Config) {
	t.Helper()
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(priv)
	require.NoError(t, err)
	// We use the libp2p TLS certificate here, just because it's convenient.
	identity, err := libp2ptls.NewIdentity(priv)
	require.NoError(t, err)
	var tlsConf tls.Config
	tlsConf.NextProtos = []string{alpn}
	tlsConf.GetConfigForClient = func(info *tls.ClientHelloInfo) (*tls.Config, error) {
		c, _ := identity.ConfigForPeer("")
		c.NextProtos = tlsConf.NextProtos
		return c, nil
	}
	return id, &tlsConf
}

func connectWithProtocol(t *testing.T, addr net.Addr, alpn string) (peer.ID, error) {
	t.Helper()
	clientKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	clientIdentity, err := libp2ptls.NewIdentity(clientKey)
	require.NoError(t, err)
	tlsConf, peerChan := clientIdentity.ConfigForPeer("")
	cconn, err := net.ListenUDP("udp4", nil)
	tlsConf.NextProtos = []string{alpn}
	require.NoError(t, err)
	c, err := quic.Dial(cconn, addr, "localhost", tlsConf, nil)
	if err != nil {
		return "", err
	}
	defer c.CloseWithError(0, "")
	require.Equal(t, alpn, c.ConnectionState().TLS.NegotiatedProtocol)
	serverID, err := peer.IDFromPublicKey(<-peerChan)
	require.NoError(t, err)
	return serverID, nil
}

func TestListener(t *testing.T) {
	t.Run("with reuseport", func(t *testing.T) {
		testListener(t, true)
	})

	t.Run("without reuseport", func(t *testing.T) {
		testListener(t, false)
	})
}

func testListener(t *testing.T, enableReuseport bool) {
	var opts []Option
	if !enableReuseport {
		opts = append(opts, DisableReuseport())
	}
	cm, err := NewConnManager([32]byte{}, opts...)
	require.NoError(t, err)

	id1, tlsConf1 := getTLSConfForProto(t, "proto1")
	ln1, err := cm.ListenQUIC(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1"), tlsConf1, nil)
	require.NoError(t, err)

	id2, tlsConf2 := getTLSConfForProto(t, "proto2")
	ln2, err := cm.ListenQUIC(
		ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/udp/%d/quic-v1", ln1.Addr().(*net.UDPAddr).Port)),
		tlsConf2,
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, ln1.Addr(), ln2.Addr())

	// Test that the right certificate is served.
	id, err := connectWithProtocol(t, ln1.Addr(), "proto1")
	require.NoError(t, err)
	require.Equal(t, id1, id)
	id, err = connectWithProtocol(t, ln1.Addr(), "proto2")
	require.NoError(t, err)
	require.Equal(t, id2, id)
	// No such protocol registered.
	_, err = connectWithProtocol(t, ln1.Addr(), "proto3")
	require.Error(t, err)

	// Now close the first listener to test that it's properly deregistered.
	require.NoError(t, ln1.Close())
	_, err = connectWithProtocol(t, ln1.Addr(), "proto1")
	require.Error(t, err)
	// connecting to the other listener should still be possible
	id, err = connectWithProtocol(t, ln1.Addr(), "proto2")
	require.NoError(t, err)
	require.Equal(t, id2, id)

	ln2.Close()
	cm.Close()

	checkClosed(t, cm)
}
