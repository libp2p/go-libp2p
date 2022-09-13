package libp2pwebtransport_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	mocknetwork "github.com/libp2p/go-libp2p/core/network/mocks"
	"github.com/libp2p/go-libp2p/core/peer"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	libp2pwebtransport "github.com/libp2p/go-libp2p/p2p/transport/webtransport"

	"github.com/golang/mock/gomock"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/multiformats/go-multibase"
	"github.com/multiformats/go-multihash"
	"github.com/stretchr/testify/require"
)

func newIdentity(t *testing.T) (peer.ID, ic.PrivKey) {
	key, _, err := ic.GenerateEd25519Key(rand.Reader)
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(key)
	require.NoError(t, err)
	return id, key
}

func randomMultihash(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	rand.Read(b)
	h, err := multihash.Encode(b, multihash.KECCAK_224)
	require.NoError(t, err)
	s, err := multibase.Encode(multibase.Base32hex, h)
	require.NoError(t, err)
	return s
}

func extractCertHashes(addr ma.Multiaddr) []string {
	var certHashesStr []string
	ma.ForEach(addr, func(c ma.Component) bool {
		if c.Protocol().Code == ma.P_CERTHASH {
			certHashesStr = append(certHashesStr, c.Value())
		}
		return true
	})
	return certHashesStr
}

func stripCertHashes(addr ma.Multiaddr) ma.Multiaddr {
	for {
		_, err := addr.ValueForProtocol(ma.P_CERTHASH)
		if err != nil {
			return addr
		}
		addr, _ = ma.SplitLast(addr)
	}
}

// create a /certhash multiaddr component using the SHA256 of foobar
func getCerthashComponent(t *testing.T, b []byte) ma.Multiaddr {
	t.Helper()
	h := sha256.Sum256(b)
	mh, err := multihash.Encode(h[:], multihash.SHA2_256)
	require.NoError(t, err)
	certStr, err := multibase.Encode(multibase.Base58BTC, mh)
	require.NoError(t, err)
	ha, err := ma.NewComponent(ma.ProtocolWithCode(ma.P_CERTHASH).Name, certStr)
	require.NoError(t, err)
	return ha
}

func TestTransport(t *testing.T) {
	serverID, serverKey := newIdentity(t)
	tr, err := libp2pwebtransport.New(serverKey, nil, network.NullResourceManager)
	require.NoError(t, err)
	defer tr.(io.Closer).Close()
	ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic/webtransport"))
	require.NoError(t, err)
	defer ln.Close()

	addrChan := make(chan ma.Multiaddr)
	go func() {
		_, clientKey := newIdentity(t)
		tr2, err := libp2pwebtransport.New(clientKey, nil, network.NullResourceManager)
		require.NoError(t, err)
		defer tr2.(io.Closer).Close()

		conn, err := tr2.Dial(context.Background(), ln.Multiaddr(), serverID)
		require.NoError(t, err)
		str, err := conn.OpenStream(context.Background())
		require.NoError(t, err)
		_, err = str.Write([]byte("foobar"))
		require.NoError(t, err)
		require.NoError(t, str.Close())

		// check RemoteMultiaddr
		_, addr, err := manet.DialArgs(ln.Multiaddr())
		require.NoError(t, err)
		_, port, err := net.SplitHostPort(addr)
		require.NoError(t, err)
		require.Equal(t, ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/udp/%s/quic/webtransport", port)), conn.RemoteMultiaddr())
		addrChan <- conn.RemoteMultiaddr()
	}()

	conn, err := ln.Accept()
	require.NoError(t, err)
	require.False(t, conn.IsClosed())
	str, err := conn.AcceptStream()
	require.NoError(t, err)
	data, err := io.ReadAll(str)
	require.NoError(t, err)
	require.Equal(t, "foobar", string(data))
	require.Equal(t, <-addrChan, conn.LocalMultiaddr())
	require.NoError(t, conn.Close())
	require.True(t, conn.IsClosed())
}

func TestHashVerification(t *testing.T) {
	serverID, serverKey := newIdentity(t)
	tr, err := libp2pwebtransport.New(serverKey, nil, network.NullResourceManager)
	require.NoError(t, err)
	defer tr.(io.Closer).Close()
	ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic/webtransport"))
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := ln.Accept()
		require.Error(t, err)
	}()

	_, clientKey := newIdentity(t)
	tr2, err := libp2pwebtransport.New(clientKey, nil, network.NullResourceManager)
	require.NoError(t, err)
	defer tr2.(io.Closer).Close()

	foobarHash := getCerthashComponent(t, []byte("foobar"))

	t.Run("fails using only a wrong hash", func(t *testing.T) {
		// replace the certificate hash in the multiaddr with a fake hash
		addr := stripCertHashes(ln.Multiaddr()).Encapsulate(foobarHash)
		_, err := tr2.Dial(context.Background(), addr, serverID)
		require.Error(t, err)
		require.Contains(t, err.Error(), "CRYPTO_ERROR (0x12a): cert hash not found")
	})

	t.Run("fails when adding a wrong hash", func(t *testing.T) {
		_, err := tr2.Dial(context.Background(), ln.Multiaddr().Encapsulate(foobarHash), serverID)
		require.Error(t, err)
	})

	require.NoError(t, ln.Close())
	<-done
}

func TestCanDial(t *testing.T) {
	valid := []ma.Multiaddr{
		ma.StringCast("/ip4/127.0.0.1/udp/1234/quic/webtransport/certhash/" + randomMultihash(t)),
		ma.StringCast("/ip6/b16b:8255:efc6:9cd5:1a54:ee86:2d7a:c2e6/udp/1234/quic/webtransport/certhash/" + randomMultihash(t)),
		ma.StringCast(fmt.Sprintf("/ip4/127.0.0.1/udp/1234/quic/webtransport/certhash/%s/certhash/%s/certhash/%s", randomMultihash(t), randomMultihash(t), randomMultihash(t))),
		ma.StringCast("/ip4/127.0.0.1/udp/1234/quic/webtransport"), // no certificate hash
	}

	invalid := []ma.Multiaddr{
		ma.StringCast("/ip4/127.0.0.1/udp/1234"),              // missing webtransport
		ma.StringCast("/ip4/127.0.0.1/udp/1234/webtransport"), // missing quic
		ma.StringCast("/ip4/127.0.0.1/tcp/1234/webtransport"), // WebTransport over TCP? Is this a joke?
	}

	_, key := newIdentity(t)
	tr, err := libp2pwebtransport.New(key, nil, network.NullResourceManager)
	require.NoError(t, err)
	defer tr.(io.Closer).Close()

	for _, addr := range valid {
		require.Truef(t, tr.CanDial(addr), "expected to be able to dial %s", addr)
	}
	for _, addr := range invalid {
		require.Falsef(t, tr.CanDial(addr), "expected to not be able to dial %s", addr)
	}
}

func TestListenAddrValidity(t *testing.T) {
	valid := []ma.Multiaddr{
		ma.StringCast("/ip6/::/udp/0/quic/webtransport/"),
		ma.StringCast("/ip4/127.0.0.1/udp/1234/quic/webtransport/"),
	}

	invalid := []ma.Multiaddr{
		ma.StringCast("/ip4/127.0.0.1/udp/1234"),              // missing webtransport
		ma.StringCast("/ip4/127.0.0.1/udp/1234/webtransport"), // missing quic
		ma.StringCast("/ip4/127.0.0.1/tcp/1234/webtransport"), // WebTransport over TCP? Is this a joke?
		ma.StringCast("/ip4/127.0.0.1/udp/1234/quic/webtransport/certhash/" + randomMultihash(t)),
	}

	_, key := newIdentity(t)
	tr, err := libp2pwebtransport.New(key, nil, network.NullResourceManager)
	require.NoError(t, err)
	defer tr.(io.Closer).Close()

	for _, addr := range valid {
		ln, err := tr.Listen(addr)
		require.NoErrorf(t, err, "expected to be able to listen on %s", addr)
		ln.Close()
	}
	for _, addr := range invalid {
		_, err := tr.Listen(addr)
		require.Errorf(t, err, "expected to not be able to listen on %s", addr)
	}
}

func TestListenerAddrs(t *testing.T) {
	_, key := newIdentity(t)
	tr, err := libp2pwebtransport.New(key, nil, network.NullResourceManager)
	require.NoError(t, err)
	defer tr.(io.Closer).Close()

	ln1, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic/webtransport"))
	require.NoError(t, err)
	ln2, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic/webtransport"))
	require.NoError(t, err)
	hashes1 := extractCertHashes(ln1.Multiaddr())
	require.Len(t, hashes1, 2)
	hashes2 := extractCertHashes(ln2.Multiaddr())
	require.Equal(t, hashes1, hashes2)
}

func TestResourceManagerDialing(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	rcmgr := mocknetwork.NewMockResourceManager(ctrl)

	addr := ma.StringCast("/ip4/9.8.7.6/udp/1234/quic/webtransport")
	p := peer.ID("foobar")

	_, key := newIdentity(t)
	tr, err := libp2pwebtransport.New(key, nil, rcmgr)
	require.NoError(t, err)
	defer tr.(io.Closer).Close()

	scope := mocknetwork.NewMockConnManagementScope(ctrl)
	rcmgr.EXPECT().OpenConnection(network.DirOutbound, false, addr).Return(scope, nil)
	scope.EXPECT().SetPeer(p).Return(errors.New("denied"))
	scope.EXPECT().Done()

	_, err = tr.Dial(context.Background(), addr, p)
	require.EqualError(t, err, "denied")
}

func TestResourceManagerListening(t *testing.T) {
	clientID, key := newIdentity(t)
	cl, err := libp2pwebtransport.New(key, nil, network.NullResourceManager)
	require.NoError(t, err)
	defer cl.(io.Closer).Close()

	t.Run("blocking the connection", func(t *testing.T) {
		serverID, key := newIdentity(t)
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		rcmgr := mocknetwork.NewMockResourceManager(ctrl)
		tr, err := libp2pwebtransport.New(key, nil, rcmgr)
		require.NoError(t, err)
		ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic/webtransport"))
		require.NoError(t, err)
		defer ln.Close()

		rcmgr.EXPECT().OpenConnection(network.DirInbound, false, gomock.Any()).DoAndReturn(func(_ network.Direction, _ bool, addr ma.Multiaddr) (network.ConnManagementScope, error) {
			_, err := addr.ValueForProtocol(ma.P_WEBTRANSPORT)
			require.NoError(t, err, "expected a WebTransport multiaddr")
			_, addrStr, err := manet.DialArgs(addr)
			require.NoError(t, err)
			host, _, err := net.SplitHostPort(addrStr)
			require.NoError(t, err)
			require.Equal(t, "127.0.0.1", host)
			return nil, errors.New("denied")
		})

		_, err = cl.Dial(context.Background(), ln.Multiaddr(), serverID)
		require.EqualError(t, err, "received status 503")
	})

	t.Run("blocking the peer", func(t *testing.T) {
		serverID, key := newIdentity(t)
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		rcmgr := mocknetwork.NewMockResourceManager(ctrl)
		tr, err := libp2pwebtransport.New(key, nil, rcmgr)
		require.NoError(t, err)
		ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic/webtransport"))
		require.NoError(t, err)
		defer ln.Close()

		serverDone := make(chan struct{})
		scope := mocknetwork.NewMockConnManagementScope(ctrl)
		rcmgr.EXPECT().OpenConnection(network.DirInbound, false, gomock.Any()).Return(scope, nil)
		scope.EXPECT().SetPeer(clientID).Return(errors.New("denied"))
		scope.EXPECT().Done().Do(func() { close(serverDone) })

		// The handshake will complete, but the server will immediately close the connection.
		conn, err := cl.Dial(context.Background(), ln.Multiaddr(), serverID)
		require.NoError(t, err)
		defer conn.Close()
		clientDone := make(chan struct{})
		go func() {
			defer close(clientDone)
			_, err = conn.AcceptStream()
			require.Error(t, err)
		}()
		select {
		case <-clientDone:
		case <-time.After(5 * time.Second):
			t.Fatal("timeout")
		}
		select {
		case <-serverDone:
		case <-time.After(5 * time.Second):
			t.Fatal("timeout")
		}
	})
}

// TODO: unify somehow. We do the same in libp2pquic.
//go:generate sh -c "mockgen -package libp2pwebtransport_test -destination mock_connection_gater_test.go github.com/libp2p/go-libp2p/core/connmgr ConnectionGater && goimports -w mock_connection_gater_test.go"

func TestConnectionGaterDialing(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	connGater := NewMockConnectionGater(ctrl)

	serverID, serverKey := newIdentity(t)
	tr, err := libp2pwebtransport.New(serverKey, nil, network.NullResourceManager)
	require.NoError(t, err)
	defer tr.(io.Closer).Close()
	ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic/webtransport"))
	require.NoError(t, err)
	defer ln.Close()

	connGater.EXPECT().InterceptSecured(network.DirOutbound, serverID, gomock.Any()).Do(func(_ network.Direction, _ peer.ID, addrs network.ConnMultiaddrs) {
		require.Equal(t, stripCertHashes(ln.Multiaddr()), addrs.RemoteMultiaddr())
	})
	_, key := newIdentity(t)
	cl, err := libp2pwebtransport.New(key, connGater, network.NullResourceManager)
	require.NoError(t, err)
	defer cl.(io.Closer).Close()
	_, err = cl.Dial(context.Background(), ln.Multiaddr(), serverID)
	require.EqualError(t, err, "secured connection gated")
}

func TestConnectionGaterInterceptAccept(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	connGater := NewMockConnectionGater(ctrl)

	serverID, serverKey := newIdentity(t)
	tr, err := libp2pwebtransport.New(serverKey, connGater, network.NullResourceManager)
	require.NoError(t, err)
	defer tr.(io.Closer).Close()
	ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic/webtransport"))
	require.NoError(t, err)
	defer ln.Close()

	connGater.EXPECT().InterceptAccept(gomock.Any()).Do(func(addrs network.ConnMultiaddrs) {
		require.Equal(t, stripCertHashes(ln.Multiaddr()), addrs.LocalMultiaddr())
		require.NotEqual(t, stripCertHashes(ln.Multiaddr()), addrs.RemoteMultiaddr())
	})

	_, key := newIdentity(t)
	cl, err := libp2pwebtransport.New(key, nil, network.NullResourceManager)
	require.NoError(t, err)
	defer cl.(io.Closer).Close()
	_, err = cl.Dial(context.Background(), ln.Multiaddr(), serverID)
	require.EqualError(t, err, "received status 403")
}

func TestConnectionGaterInterceptSecured(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	connGater := NewMockConnectionGater(ctrl)

	serverID, serverKey := newIdentity(t)
	tr, err := libp2pwebtransport.New(serverKey, connGater, network.NullResourceManager)
	require.NoError(t, err)
	defer tr.(io.Closer).Close()
	ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic/webtransport"))
	require.NoError(t, err)
	defer ln.Close()

	clientID, key := newIdentity(t)
	cl, err := libp2pwebtransport.New(key, nil, network.NullResourceManager)
	require.NoError(t, err)
	defer cl.(io.Closer).Close()

	connGater.EXPECT().InterceptAccept(gomock.Any()).Return(true)
	connGater.EXPECT().InterceptSecured(network.DirInbound, clientID, gomock.Any()).Do(func(_ network.Direction, _ peer.ID, addrs network.ConnMultiaddrs) {
		require.Equal(t, stripCertHashes(ln.Multiaddr()), addrs.LocalMultiaddr())
		require.NotEqual(t, stripCertHashes(ln.Multiaddr()), addrs.RemoteMultiaddr())
	})
	// The handshake will complete, but the server will immediately close the connection.
	conn, err := cl.Dial(context.Background(), ln.Multiaddr(), serverID)
	require.NoError(t, err)
	defer conn.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err = conn.AcceptStream()
		require.Error(t, err)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
}

func getTLSConf(t *testing.T, ip net.IP, start, end time.Time) *tls.Config {
	t.Helper()
	certTempl := &x509.Certificate{
		SerialNumber:          big.NewInt(1234),
		Subject:               pkix.Name{Organization: []string{"webtransport"}},
		NotBefore:             start,
		NotAfter:              end,
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{ip},
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caBytes, err := x509.CreateCertificate(rand.Reader, certTempl, certTempl, &priv.PublicKey, priv)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(caBytes)
	require.NoError(t, err)
	return &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{cert.Raw},
			PrivateKey:  priv,
			Leaf:        cert,
		}},
	}
}

func TestStaticTLSConf(t *testing.T) {
	tlsConf := getTLSConf(t, net.ParseIP("127.0.0.1"), time.Now(), time.Now().Add(365*24*time.Hour))

	serverID, serverKey := newIdentity(t)
	tr, err := libp2pwebtransport.New(serverKey, nil, network.NullResourceManager, libp2pwebtransport.WithTLSConfig(tlsConf))
	require.NoError(t, err)
	defer tr.(io.Closer).Close()
	ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic/webtransport"))
	require.NoError(t, err)
	defer ln.Close()
	require.Empty(t, extractCertHashes(ln.Multiaddr()), "listener address shouldn't contain any certhash")

	t.Run("fails when the certificate is invalid", func(t *testing.T) {
		_, key := newIdentity(t)
		cl, err := libp2pwebtransport.New(key, nil, network.NullResourceManager)
		require.NoError(t, err)
		defer cl.(io.Closer).Close()

		_, err = cl.Dial(context.Background(), ln.Multiaddr(), serverID)
		require.Error(t, err)
		if !strings.Contains(err.Error(), "certificate is not trusted") &&
			!strings.Contains(err.Error(), "certificate signed by unknown authority") {
			t.Fatalf("expected a certificate error, got %+v", err)
		}
	})

	t.Run("fails when dialing with a wrong certhash", func(t *testing.T) {
		_, key := newIdentity(t)
		cl, err := libp2pwebtransport.New(key, nil, network.NullResourceManager)
		require.NoError(t, err)
		defer cl.(io.Closer).Close()

		addr := ln.Multiaddr().Encapsulate(getCerthashComponent(t, []byte("foo")))
		_, err = cl.Dial(context.Background(), addr, serverID)
		require.Error(t, err)
		require.Contains(t, err.Error(), "cert hash not found")
	})

	t.Run("accepts a valid TLS certificate", func(t *testing.T) {
		_, key := newIdentity(t)
		store := x509.NewCertPool()
		store.AddCert(tlsConf.Certificates[0].Leaf)
		tlsConf := &tls.Config{RootCAs: store}
		cl, err := libp2pwebtransport.New(key, nil, network.NullResourceManager, libp2pwebtransport.WithTLSClientConfig(tlsConf))
		require.NoError(t, err)
		defer cl.(io.Closer).Close()

		require.True(t, cl.CanDial(ln.Multiaddr()))
		conn, err := cl.Dial(context.Background(), ln.Multiaddr(), serverID)
		require.NoError(t, err)
		defer conn.Close()
	})
}

func TestAcceptQueueFilledUp(t *testing.T) {
	serverID, serverKey := newIdentity(t)
	tr, err := libp2pwebtransport.New(serverKey, nil, network.NullResourceManager)
	require.NoError(t, err)
	defer tr.(io.Closer).Close()
	ln, err := tr.Listen(ma.StringCast("/ip4/127.0.0.1/udp/0/quic/webtransport"))
	require.NoError(t, err)
	defer ln.Close()

	newConn := func() (tpt.CapableConn, error) {
		t.Helper()
		_, key := newIdentity(t)
		cl, err := libp2pwebtransport.New(key, nil, network.NullResourceManager)
		require.NoError(t, err)
		defer cl.(io.Closer).Close()
		return cl.Dial(context.Background(), ln.Multiaddr(), serverID)
	}

	for i := 0; i < 16; i++ {
		conn, err := newConn()
		require.NoError(t, err)
		defer conn.Close()
	}

	conn, err := newConn()
	if err == nil {
		_, err = conn.AcceptStream()
	}
	require.Error(t, err)
}
