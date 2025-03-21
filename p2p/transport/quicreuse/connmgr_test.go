package quicreuse

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

func checkClosed(t *testing.T, cm *ConnManager) {
	for _, r := range []*reuse{cm.reuseUDP4, cm.reuseUDP6} {
		if r == nil {
			continue
		}
		r.mutex.Lock()
		for _, tr := range r.globalListeners {
			require.Zero(t, tr.GetCount())
		}
		for _, trs := range r.unicast {
			for _, tr := range trs {
				require.Zero(t, tr.GetCount())
			}
		}
		r.mutex.Unlock()
	}
	require.Eventually(t, func() bool { return !isGarbageCollectorRunning() }, 200*time.Millisecond, 10*time.Millisecond)
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
	cm, err := NewConnManager(quic.StatelessResetKey{}, quic.TokenGeneratorKey{}, opts...)
	require.NoError(t, err)
	defer checkClosed(t, cm)
	defer cm.Close()

	const alpn = "proto"

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
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows. Windows doesn't support these optimizations")
	}
	cm, err := NewConnManager(quic.StatelessResetKey{}, quic.TokenGeneratorKey{}, DisableReuseport())
	require.NoError(t, err)
	defer cm.Close()

	raddr := ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1")

	naddr, _, err := FromQuicMultiaddr(raddr)
	require.NoError(t, err)
	netw, _, err := manet.DialArgs(raddr)
	require.NoError(t, err)

	_, err = cm.ListenQUIC(raddr, &tls.Config{NextProtos: []string{"proto"}}, nil)
	require.NoError(t, err)
	quicTr, err := cm.transportForListen(nil, netw, naddr)
	require.NoError(t, err)
	defer quicTr.Close()
	if _, ok := quicTr.(*singleOwnerTransport).packetConn.(quic.OOBCapablePacketConn); !ok {
		t.Fatal("connection passed to quic-go cannot be type asserted to a *net.UDPConn")
	}
}

func TestAcceptErrorGetCleanedUp(t *testing.T) {
	raddr := ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1")

	cm, err := NewConnManager(quic.StatelessResetKey{}, quic.TokenGeneratorKey{}, DisableReuseport())
	require.NoError(t, err)
	defer cm.Close()

	originalNumberOfGoroutines := runtime.NumGoroutine()
	t.Log("num goroutines:", originalNumberOfGoroutines)

	// This spawns a background goroutine for the listener
	l, err := cm.ListenQUIC(raddr, &tls.Config{NextProtos: []string{"proto"}}, nil)
	require.NoError(t, err)

	// We spawned a goroutine for the listener
	require.Greater(t, runtime.NumGoroutine(), originalNumberOfGoroutines)
	l.Close()

	// Now make sure we have less goroutines than before
	// Manually doing the same as require.Eventually, except avoiding adding a goroutine
	goRoutinesCleanedUp := false
	for i := 0; i < 50; i++ {
		t.Log("num goroutines:", runtime.NumGoroutine())
		if runtime.NumGoroutine() <= originalNumberOfGoroutines {
			goRoutinesCleanedUp = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	require.True(t, goRoutinesCleanedUp, "goroutines were not cleaned up")
}

// The connection passed to quic-go needs to be type-assertable to a net.UDPConn,
// in order to enable features like batch processing and ECN.
func TestConnectionPassedToQUICForDialing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows. Windows doesn't support these optimizations")
	}
	cm, err := NewConnManager(quic.StatelessResetKey{}, quic.TokenGeneratorKey{}, DisableReuseport())
	require.NoError(t, err)
	defer cm.Close()

	raddr := ma.StringCast("/ip4/127.0.0.1/udp/1234/quic-v1")

	naddr, _, err := FromQuicMultiaddr(raddr)
	require.NoError(t, err)
	netw, _, err := manet.DialArgs(raddr)
	require.NoError(t, err)

	quicTr, err := cm.TransportForDial(netw, naddr)

	require.NoError(t, err, "dial error")
	defer quicTr.Close()
	if _, ok := quicTr.(*singleOwnerTransport).packetConn.(quic.OOBCapablePacketConn); !ok {
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
	c, err := quic.Dial(context.Background(), cconn, addr, tlsConf, nil)
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
	cm, err := NewConnManager(quic.StatelessResetKey{}, quic.TokenGeneratorKey{}, opts...)
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

func TestExternalTransport(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	require.NoError(t, err)
	defer conn.Close()
	port := conn.LocalAddr().(*net.UDPAddr).Port
	tr := &quic.Transport{Conn: conn}
	defer tr.Close()

	cm, err := NewConnManager(quic.StatelessResetKey{}, quic.TokenGeneratorKey{})
	require.NoError(t, err)
	doneWithTr, err := cm.LendTransport("udp4", &wrappedQUICTransport{tr}, conn)
	require.NoError(t, err)

	// make sure this transport is used when listening on the same port
	ln, err := cm.ListenQUICAndAssociate(
		"quic",
		ma.StringCast(fmt.Sprintf("/ip4/0.0.0.0/udp/%d", port)),
		&tls.Config{NextProtos: []string{"libp2p"}},
		func(quic.Connection, uint64) bool { return false },
	)
	require.NoError(t, err)
	defer ln.Close()
	require.Equal(t, port, ln.Addr().(*net.UDPAddr).Port)

	// make sure this transport is used when dialing out
	udpLn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer udpLn.Close()
	addrChan := make(chan net.Addr, 1)
	go func() {
		_, addr, _ := udpLn.ReadFrom(make([]byte, 2000))
		addrChan <- addr
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = cm.DialQUIC(
		ctx,
		ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/udp/%d/quic-v1", udpLn.LocalAddr().(*net.UDPAddr).Port)),
		&tls.Config{NextProtos: []string{"libp2p"}},
		func(quic.Connection, uint64) bool { return false },
	)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	select {
	case addr := <-addrChan:
		require.Equal(t, port, addr.(*net.UDPAddr).Port)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	cm.Close()
	select {
	case <-doneWithTr:
	default:
		t.Fatal("doneWithTr not closed")
	}
}

func TestAssociate(t *testing.T) {
	testAssociate := func(lnAddr1, lnAddr2 ma.Multiaddr, dialAddr *net.UDPAddr) {
		cm, err := NewConnManager(quic.StatelessResetKey{}, quic.TokenGeneratorKey{})
		require.NoError(t, err)
		defer cm.Close()

		lp2pTLS := &tls.Config{NextProtos: []string{"libp2p"}}
		assoc1 := "test-1"
		ln1, err := cm.ListenQUICAndAssociate(assoc1, lnAddr1, lp2pTLS, nil)
		require.NoError(t, err)
		defer ln1.Close()
		addrs := ln1.Multiaddrs()
		require.Len(t, addrs, 1)

		addr := addrs[0]
		assoc2 := "test-2"
		h3TLS := &tls.Config{NextProtos: []string{"h3"}}
		ln2, err := cm.ListenQUICAndAssociate(assoc2, addr, h3TLS, nil)
		require.NoError(t, err)
		defer ln2.Close()

		tr1, err := cm.TransportWithAssociationForDial(assoc1, "udp4", dialAddr)
		require.NoError(t, err)
		defer tr1.Close()
		require.Equal(t, tr1.LocalAddr().String(), ln1.Addr().String())

		tr2, err := cm.TransportWithAssociationForDial(assoc2, "udp4", dialAddr)
		require.NoError(t, err)
		defer tr2.Close()
		require.Equal(t, tr2.LocalAddr().String(), ln2.Addr().String())

		ln3, err := cm.ListenQUICAndAssociate(assoc1, lnAddr2, lp2pTLS, nil)
		require.NoError(t, err)
		defer ln3.Close()

		// an unused association should also return the same transport
		// association is only a preference for a specific transport, not an exclusion criteria
		tr3, err := cm.TransportWithAssociationForDial("unused", "udp4", dialAddr)
		require.NoError(t, err)
		defer tr3.Close()
		require.Contains(t, []string{ln2.Addr().String(), ln3.Addr().String()}, tr3.LocalAddr().String())
	}

	t.Run("MultipleUnspecifiedListeners", func(t *testing.T) {
		testAssociate(ma.StringCast("/ip4/0.0.0.0/udp/0/quic-v1"),
			ma.StringCast("/ip4/0.0.0.0/udp/0/quic-v1"),
			&net.UDPAddr{IP: net.IPv4(1, 1, 1, 1), Port: 1})
	})
	t.Run("MultipleSpecificListeners", func(t *testing.T) {
		testAssociate(ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1"),
			ma.StringCast("/ip4/127.0.0.1/udp/0/quic-v1"),
			&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1},
		)
	})
}
