package libp2pquic

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	mrand "math/rand"
	"net"
	"sync/atomic"
	"testing"
	"time"

	ic "github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	tpt "github.com/libp2p/go-libp2p-core/transport"

	mocknetwork "github.com/libp2p/go-libp2p-testing/mocks/network"

	quicproxy "github.com/lucas-clemente/quic-go/integrationtests/tools/proxy"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
)

//go:generate sh -c "mockgen -package libp2pquic -destination mock_connection_gater_test.go github.com/libp2p/go-libp2p-core/connmgr ConnectionGater && goimports -w mock_connection_gater_test.go"

func createPeer(t *testing.T) (peer.ID, ic.PrivKey) {
	var priv ic.PrivKey
	var err error
	switch mrand.Int() % 4 {
	case 0:
		priv, _, err = ic.GenerateECDSAKeyPair(rand.Reader)
	case 1:
		priv, _, err = ic.GenerateRSAKeyPair(2048, rand.Reader)
	case 2:
		priv, _, err = ic.GenerateEd25519Key(rand.Reader)
	case 3:
		priv, _, err = ic.GenerateSecp256k1Key(rand.Reader)
	}
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(priv)
	require.NoError(t, err)
	t.Logf("using a %s key: %s", priv.Type(), id.Pretty())
	return id, priv
}

func runServer(t *testing.T, tr tpt.Transport, addr string) tpt.Listener {
	t.Helper()
	ln, err := tr.Listen(ma.StringCast(addr))
	require.NoError(t, err)
	return ln
}

func TestHandshake(t *testing.T) {
	serverID, serverKey := createPeer(t)
	clientID, clientKey := createPeer(t)
	serverTransport, err := NewTransport(serverKey, nil, nil, nil)
	require.NoError(t, err)
	defer serverTransport.(io.Closer).Close()

	handshake := func(t *testing.T, ln tpt.Listener) {
		clientTransport, err := NewTransport(clientKey, nil, nil, nil)
		require.NoError(t, err)
		defer clientTransport.(io.Closer).Close()
		conn, err := clientTransport.Dial(context.Background(), ln.Multiaddr(), serverID)
		require.NoError(t, err)
		defer conn.Close()
		serverConn, err := ln.Accept()
		require.NoError(t, err)
		defer serverConn.Close()

		require.Equal(t, conn.LocalPeer(), clientID)
		require.True(t, conn.LocalPrivateKey().Equals(clientKey), "local private key doesn't match")
		require.Equal(t, conn.RemotePeer(), serverID)
		require.True(t, conn.RemotePublicKey().Equals(serverKey.GetPublic()), "remote public key doesn't match")

		require.Equal(t, serverConn.LocalPeer(), serverID)
		require.True(t, serverConn.LocalPrivateKey().Equals(serverKey), "local private key doesn't match")
		require.Equal(t, serverConn.RemotePeer(), clientID)
		require.True(t, serverConn.RemotePublicKey().Equals(clientKey.GetPublic()), "remote public key doesn't match")
	}

	t.Run("on IPv4", func(t *testing.T) {
		ln := runServer(t, serverTransport, "/ip4/127.0.0.1/udp/0/quic")
		defer ln.Close()
		handshake(t, ln)
	})

	t.Run("on IPv6", func(t *testing.T) {
		ln := runServer(t, serverTransport, "/ip6/::1/udp/0/quic")
		defer ln.Close()
		handshake(t, ln)
	})
}

func TestResourceManagerSuccess(t *testing.T) {
	serverID, serverKey := createPeer(t)
	clientID, clientKey := createPeer(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	serverRcmgr := mocknetwork.NewMockResourceManager(ctrl)
	serverTransport, err := NewTransport(serverKey, nil, nil, serverRcmgr)
	require.NoError(t, err)
	defer serverTransport.(io.Closer).Close()
	ln, err := serverTransport.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic"))
	require.NoError(t, err)
	defer ln.Close()

	clientRcmgr := mocknetwork.NewMockResourceManager(ctrl)
	clientTransport, err := NewTransport(clientKey, nil, nil, clientRcmgr)
	require.NoError(t, err)
	defer clientTransport.(io.Closer).Close()

	connChan := make(chan tpt.CapableConn)
	serverConnScope := mocknetwork.NewMockConnManagementScope(ctrl)
	go func() {
		serverRcmgr.EXPECT().OpenConnection(network.DirInbound, false).Return(serverConnScope, nil)
		serverConnScope.EXPECT().SetPeer(clientID)
		serverConn, err := ln.Accept()
		require.NoError(t, err)
		connChan <- serverConn
	}()

	connScope := mocknetwork.NewMockConnManagementScope(ctrl)
	clientRcmgr.EXPECT().OpenConnection(network.DirOutbound, false).Return(connScope, nil)
	connScope.EXPECT().SetPeer(serverID)
	conn, err := clientTransport.Dial(context.Background(), ln.Multiaddr(), serverID)
	require.NoError(t, err)
	serverConn := <-connChan
	t.Log("received conn")
	connScope.EXPECT().Done().MinTimes(1) // for dialed connections, we might call Done multiple times
	conn.Close()
	serverConnScope.EXPECT().Done()
	serverConn.Close()
}

func TestResourceManagerDialDenied(t *testing.T) {
	_, clientKey := createPeer(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	rcmgr := mocknetwork.NewMockResourceManager(ctrl)
	clientTransport, err := NewTransport(clientKey, nil, nil, rcmgr)
	require.NoError(t, err)
	defer clientTransport.(io.Closer).Close()

	connScope := mocknetwork.NewMockConnManagementScope(ctrl)
	rcmgr.EXPECT().OpenConnection(network.DirOutbound, false).Return(connScope, nil)
	rerr := errors.New("nope")
	p := peer.ID("server")
	connScope.EXPECT().SetPeer(p).Return(rerr)
	connScope.EXPECT().Done()

	_, err = clientTransport.Dial(context.Background(), ma.StringCast("/ip4/127.0.0.1/udp/1234/quic"), p)
	require.ErrorIs(t, err, rerr)
}

func TestResourceManagerAcceptDenied(t *testing.T) {
	serverID, serverKey := createPeer(t)
	clientID, clientKey := createPeer(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	clientRcmgr := mocknetwork.NewMockResourceManager(ctrl)
	clientTransport, err := NewTransport(clientKey, nil, nil, clientRcmgr)
	require.NoError(t, err)
	defer clientTransport.(io.Closer).Close()

	serverRcmgr := mocknetwork.NewMockResourceManager(ctrl)
	serverConnScope := mocknetwork.NewMockConnManagementScope(ctrl)
	rerr := errors.New("denied")
	gomock.InOrder(
		serverRcmgr.EXPECT().OpenConnection(network.DirInbound, false).Return(serverConnScope, nil),
		serverConnScope.EXPECT().SetPeer(clientID).Return(rerr),
		serverConnScope.EXPECT().Done(),
	)
	serverTransport, err := NewTransport(serverKey, nil, nil, serverRcmgr)
	require.NoError(t, err)
	defer serverTransport.(io.Closer).Close()
	ln, err := serverTransport.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic"))
	require.NoError(t, err)
	defer ln.Close()
	connChan := make(chan tpt.CapableConn)
	go func() {
		ln.Accept()
		close(connChan)
	}()

	clientConnScope := mocknetwork.NewMockConnManagementScope(ctrl)
	clientRcmgr.EXPECT().OpenConnection(network.DirOutbound, false).Return(clientConnScope, nil)
	clientConnScope.EXPECT().SetPeer(serverID)
	// In rare instances, the connection gating error will already occur on Dial.
	// In that case, Done is called on the connection scope.
	clientConnScope.EXPECT().Done().MaxTimes(1)
	conn, err := clientTransport.Dial(context.Background(), ln.Multiaddr(), serverID)
	// In rare instances, the connection gating error will already occur on Dial.
	if err == nil {
		_, err = conn.AcceptStream()
		require.Error(t, err)
	}
	select {
	case <-connChan:
		t.Fatal("didn't expect to accept a connection")
	default:
	}
}

func TestStreams(t *testing.T) {
	serverID, serverKey := createPeer(t)
	_, clientKey := createPeer(t)

	serverTransport, err := NewTransport(serverKey, nil, nil, nil)
	require.NoError(t, err)
	defer serverTransport.(io.Closer).Close()
	ln := runServer(t, serverTransport, "/ip4/127.0.0.1/udp/0/quic")
	defer ln.Close()

	clientTransport, err := NewTransport(clientKey, nil, nil, nil)
	require.NoError(t, err)
	defer clientTransport.(io.Closer).Close()
	conn, err := clientTransport.Dial(context.Background(), ln.Multiaddr(), serverID)
	require.NoError(t, err)
	defer conn.Close()
	serverConn, err := ln.Accept()
	require.NoError(t, err)
	defer serverConn.Close()

	str, err := conn.OpenStream(context.Background())
	require.NoError(t, err)
	_, err = str.Write([]byte("foobar"))
	require.NoError(t, err)
	str.Close()
	sstr, err := serverConn.AcceptStream()
	require.NoError(t, err)
	data, err := ioutil.ReadAll(sstr)
	require.NoError(t, err)
	require.Equal(t, data, []byte("foobar"))
}

func TestHandshakeFailPeerIDMismatch(t *testing.T) {
	_, serverKey := createPeer(t)
	_, clientKey := createPeer(t)
	thirdPartyID, _ := createPeer(t)

	serverTransport, err := NewTransport(serverKey, nil, nil, nil)
	require.NoError(t, err)
	defer serverTransport.(io.Closer).Close()
	ln := runServer(t, serverTransport, "/ip4/127.0.0.1/udp/0/quic")

	clientTransport, err := NewTransport(clientKey, nil, nil, nil)
	require.NoError(t, err)
	// dial, but expect the wrong peer ID
	_, err = clientTransport.Dial(context.Background(), ln.Multiaddr(), thirdPartyID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "CRYPTO_ERROR")
	defer clientTransport.(io.Closer).Close()

	acceptErr := make(chan error)
	go func() {
		_, err := ln.Accept()
		acceptErr <- err
	}()

	select {
	case <-acceptErr:
		t.Fatal("didn't expect Accept to return before being closed")
	case <-time.After(100 * time.Millisecond):
	}

	require.NoError(t, ln.Close())
	require.Error(t, <-acceptErr)
}

func TestConnectionGating(t *testing.T) {
	serverID, serverKey := createPeer(t)
	_, clientKey := createPeer(t)

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	cg := NewMockConnectionGater(mockCtrl)

	t.Run("accepted connections", func(t *testing.T) {
		serverTransport, err := NewTransport(serverKey, nil, cg, nil)
		defer serverTransport.(io.Closer).Close()
		require.NoError(t, err)
		ln := runServer(t, serverTransport, "/ip4/127.0.0.1/udp/0/quic")
		defer ln.Close()

		cg.EXPECT().InterceptAccept(gomock.Any())

		accepted := make(chan struct{})
		go func() {
			defer close(accepted)
			_, err := ln.Accept()
			require.NoError(t, err)
		}()

		clientTransport, err := NewTransport(clientKey, nil, nil, nil)
		require.NoError(t, err)
		defer clientTransport.(io.Closer).Close()
		// make sure that connection attempts fails
		conn, err := clientTransport.Dial(context.Background(), ln.Multiaddr(), serverID)
		// In rare instances, the connection gating error will already occur on Dial.
		// In most cases, it will be returned by AcceptStream.
		if err == nil {
			_, err = conn.AcceptStream()
		}
		require.Contains(t, err.Error(), "connection gated")

		// now allow the address and make sure the connection goes through
		cg.EXPECT().InterceptAccept(gomock.Any()).Return(true)
		cg.EXPECT().InterceptSecured(gomock.Any(), gomock.Any(), gomock.Any()).Return(true)
		clientTransport.(*transport).clientConfig.HandshakeIdleTimeout = 2 * time.Second
		conn, err = clientTransport.Dial(context.Background(), ln.Multiaddr(), serverID)
		require.NoError(t, err)
		defer conn.Close()
		require.Eventually(t, func() bool {
			select {
			case <-accepted:
				return true
			default:
				return false
			}
		}, time.Second, 10*time.Millisecond)
	})

	t.Run("secured connections", func(t *testing.T) {
		serverTransport, err := NewTransport(serverKey, nil, nil, nil)
		require.NoError(t, err)
		defer serverTransport.(io.Closer).Close()
		ln := runServer(t, serverTransport, "/ip4/127.0.0.1/udp/0/quic")
		defer ln.Close()

		cg := NewMockConnectionGater(mockCtrl)
		cg.EXPECT().InterceptSecured(gomock.Any(), gomock.Any(), gomock.Any())

		clientTransport, err := NewTransport(clientKey, nil, cg, nil)
		require.NoError(t, err)
		defer clientTransport.(io.Closer).Close()

		// make sure that connection attempts fails
		_, err = clientTransport.Dial(context.Background(), ln.Multiaddr(), serverID)
		require.Error(t, err)
		require.Contains(t, err.Error(), "connection gated")

		// now allow the peerId and make sure the connection goes through
		cg.EXPECT().InterceptSecured(gomock.Any(), gomock.Any(), gomock.Any()).Return(true)
		clientTransport.(*transport).clientConfig.HandshakeIdleTimeout = 2 * time.Second
		conn, err := clientTransport.Dial(context.Background(), ln.Multiaddr(), serverID)
		require.NoError(t, err)
		conn.Close()
	})
}

func TestDialTwo(t *testing.T) {
	serverID, serverKey := createPeer(t)
	_, clientKey := createPeer(t)
	serverID2, serverKey2 := createPeer(t)

	serverTransport, err := NewTransport(serverKey, nil, nil, nil)
	require.NoError(t, err)
	defer serverTransport.(io.Closer).Close()
	ln1 := runServer(t, serverTransport, "/ip4/127.0.0.1/udp/0/quic")
	defer ln1.Close()
	serverTransport2, err := NewTransport(serverKey2, nil, nil, nil)
	require.NoError(t, err)
	defer serverTransport2.(io.Closer).Close()
	ln2 := runServer(t, serverTransport2, "/ip4/127.0.0.1/udp/0/quic")
	defer ln2.Close()

	data := bytes.Repeat([]byte{'a'}, 5*1<<20) // 5 MB
	// wait for both servers to accept a connection
	// then send some data
	go func() {
		serverConn1, err := ln1.Accept()
		require.NoError(t, err)
		serverConn2, err := ln2.Accept()
		require.NoError(t, err)

		for _, c := range []tpt.CapableConn{serverConn1, serverConn2} {
			go func(conn tpt.CapableConn) {
				str, err := conn.OpenStream(context.Background())
				require.NoError(t, err)
				defer str.Close()
				_, err = str.Write(data)
				require.NoError(t, err)
			}(c)
		}
	}()

	clientTransport, err := NewTransport(clientKey, nil, nil, nil)
	require.NoError(t, err)
	defer clientTransport.(io.Closer).Close()
	c1, err := clientTransport.Dial(context.Background(), ln1.Multiaddr(), serverID)
	require.NoError(t, err)
	defer c1.Close()
	c2, err := clientTransport.Dial(context.Background(), ln2.Multiaddr(), serverID2)
	require.NoError(t, err)
	defer c2.Close()

	done := make(chan struct{}, 2)
	// receive the data on both connections at the same time
	for _, c := range []tpt.CapableConn{c1, c2} {
		go func(conn tpt.CapableConn) {
			str, err := conn.AcceptStream()
			require.NoError(t, err)
			str.CloseWrite()
			d, err := ioutil.ReadAll(str)
			require.NoError(t, err)
			require.Equal(t, d, data)
			done <- struct{}{}
		}(c)
	}

	for i := 0; i < 2; i++ {
		require.Eventually(t, func() bool {
			select {
			case <-done:
				return true
			default:
				return false
			}
		}, 15*time.Second, 50*time.Millisecond)
	}
}

func TestStatelessReset(t *testing.T) {
	origGarbageCollectInterval := garbageCollectInterval
	origMaxUnusedDuration := maxUnusedDuration

	garbageCollectInterval = 50 * time.Millisecond
	maxUnusedDuration = 0

	t.Cleanup(func() {
		garbageCollectInterval = origGarbageCollectInterval
		maxUnusedDuration = origMaxUnusedDuration
	})

	serverID, serverKey := createPeer(t)
	_, clientKey := createPeer(t)

	serverTransport, err := NewTransport(serverKey, nil, nil, nil)
	require.NoError(t, err)
	defer serverTransport.(io.Closer).Close()
	ln := runServer(t, serverTransport, "/ip4/127.0.0.1/udp/0/quic")

	var drop uint32
	serverPort := ln.Addr().(*net.UDPAddr).Port
	proxy, err := quicproxy.NewQuicProxy("localhost:0", &quicproxy.Opts{
		RemoteAddr: fmt.Sprintf("localhost:%d", serverPort),
		DropPacket: func(quicproxy.Direction, []byte) bool {
			return atomic.LoadUint32(&drop) > 0
		},
	})
	require.NoError(t, err)
	defer proxy.Close()

	// establish a connection
	clientTransport, err := NewTransport(clientKey, nil, nil, nil)
	require.NoError(t, err)
	defer clientTransport.(io.Closer).Close()
	proxyAddr, err := toQuicMultiaddr(proxy.LocalAddr())
	require.NoError(t, err)
	conn, err := clientTransport.Dial(context.Background(), proxyAddr, serverID)
	require.NoError(t, err)
	connChan := make(chan tpt.CapableConn)
	go func() {
		conn, err := ln.Accept()
		require.NoError(t, err)
		str, err := conn.OpenStream(context.Background())
		require.NoError(t, err)
		str.Write([]byte("foobar"))
		connChan <- conn
	}()

	str, err := conn.AcceptStream()
	require.NoError(t, err)
	_, err = str.Read(make([]byte, 6))
	require.NoError(t, err)

	// Stop forwarding packets and close the server.
	// This prevents the CONNECTION_CLOSE from reaching the client.
	atomic.StoreUint32(&drop, 1)
	ln.Close()
	(<-connChan).Close()
	// require.NoError(t, ln.Close())
	time.Sleep(2000 * time.Millisecond) // give the kernel some time to free the UDP port
	ln = runServer(t, serverTransport, fmt.Sprintf("/ip4/127.0.0.1/udp/%d/quic", serverPort))
	defer ln.Close()
	// Now that the new server is up, re-enable packet forwarding.
	atomic.StoreUint32(&drop, 0)

	// Trigger something (not too small) to be sent, so that we receive the stateless reset.
	// The new server doesn't have any state for the previously established connection.
	// We expect it to send a stateless reset.
	_, rerr := str.Write([]byte("Lorem ipsum dolor sit amet."))
	if rerr == nil {
		_, rerr = str.Read([]byte{0, 0})
	}
	require.Error(t, rerr)
	require.Contains(t, rerr.Error(), "received a stateless reset")
}

func TestHolePunching(t *testing.T) {
	serverID, serverKey := createPeer(t)
	clientID, clientKey := createPeer(t)

	t1, err := NewTransport(serverKey, nil, nil, nil)
	require.NoError(t, err)
	defer t1.(io.Closer).Close()
	laddr, err := ma.NewMultiaddr("/ip4/127.0.0.1/udp/0/quic")
	require.NoError(t, err)
	ln1, err := t1.Listen(laddr)
	require.NoError(t, err)
	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		_, err := ln1.Accept()
		require.Error(t, err, "didn't expect to accept any connections")
	}()

	t2, err := NewTransport(clientKey, nil, nil, nil)
	require.NoError(t, err)
	defer t2.(io.Closer).Close()
	ln2, err := t2.Listen(laddr)
	require.NoError(t, err)
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		_, err := ln2.Accept()
		require.Error(t, err, "didn't expect to accept any connections")
	}()
	connChan := make(chan tpt.CapableConn)
	go func() {
		conn, err := t2.Dial(
			network.WithSimultaneousConnect(context.Background(), false, ""),
			ln1.Multiaddr(),
			serverID,
		)
		require.NoError(t, err)
		connChan <- conn
	}()
	// Make sure the server role (the dial on t2) has progressed far enough.
	// If it hasn't created the hole punch map entry, the connection will be accepted as a regular connection,
	// which would make this test fail.
	require.Eventually(t, func() bool {
		tr := t2.(*transport)
		tr.holePunchingMx.Lock()
		defer tr.holePunchingMx.Unlock()
		return len(tr.holePunching) > 0
	}, time.Second, 10*time.Millisecond)

	conn1, err := t1.Dial(
		network.WithSimultaneousConnect(context.Background(), true, ""),
		ln2.Multiaddr(),
		clientID,
	)
	require.NoError(t, err)
	defer conn1.Close()
	require.Equal(t, conn1.RemotePeer(), clientID)
	var conn2 tpt.CapableConn
	require.Eventually(t, func() bool {
		select {
		case conn2 = <-connChan:
			return true
		default:
			return false
		}
	}, 100*time.Millisecond, 10*time.Millisecond)
	defer conn2.Close()
	require.Equal(t, conn2.RemotePeer(), serverID)
	ln1.Close()
	ln2.Close()
	<-done1
	<-done2
}
