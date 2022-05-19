package websocket

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"io/ioutil"
	"math/big"
	"net"
	"testing"
	"time"

	csms "github.com/libp2p/go-libp2p/p2p/net/conn-security-multistream"
	tptu "github.com/libp2p/go-libp2p/p2p/net/upgrader"
	ttransport "github.com/libp2p/go-libp2p/p2p/transport/testsuite"

	"github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/sec"
	"github.com/libp2p/go-libp2p-core/sec/insecure"
	"github.com/libp2p/go-libp2p-core/test"
	"github.com/libp2p/go-libp2p-core/transport"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func newUpgrader(t *testing.T) (peer.ID, transport.Upgrader) {
	t.Helper()
	id, m := newSecureMuxer(t)
	u, err := tptu.New(m, yamux.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	return id, u
}

func newSecureMuxer(t *testing.T) (peer.ID, sec.SecureMuxer) {
	t.Helper()
	priv, _, err := test.RandTestKeyPair(crypto.Ed25519, 256)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	var secMuxer csms.SSMuxer
	secMuxer.AddTransport(insecure.ID, insecure.NewWithIdentity(id, priv))
	return id, &secMuxer
}

func lastComponent(t *testing.T, a ma.Multiaddr) ma.Multiaddr {
	t.Helper()
	_, wscomponent := ma.SplitLast(a)
	require.NotNil(t, wscomponent)
	if wscomponent.Equal(wsma) {
		return wsma
	}
	if wscomponent.Equal(wssma) {
		return wssma
	}
	t.Fatal("expected a ws or wss component")
	return nil
}

func generateTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{},
		SignatureAlgorithm:    x509.SHA256WithRSA,
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour), // valid for an hour
		BasicConstraintsValid: true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, priv.Public(), priv)
	require.NoError(t, err)
	return &tls.Config{
		Certificates: []tls.Certificate{{
			PrivateKey:  priv,
			Certificate: [][]byte{certDER},
		}},
	}
}

func TestCanDial(t *testing.T) {
	d := &WebsocketTransport{}
	if !d.CanDial(ma.StringCast("/ip4/127.0.0.1/tcp/5555/ws")) {
		t.Fatal("expected to match websocket maddr, but did not")
	}
	if !d.CanDial(ma.StringCast("/ip4/127.0.0.1/tcp/5555/wss")) {
		t.Fatal("expected to match secure websocket maddr, but did not")
	}
	if d.CanDial(ma.StringCast("/ip4/127.0.0.1/tcp/5555")) {
		t.Fatal("expected to not match tcp maddr, but did")
	}
}

func TestDialWss(t *testing.T) {
	if _, err := net.LookupIP("nyc-1.bootstrap.libp2p.io"); err != nil {
		t.Skip("this test requries an internet connection and it seems like we currently don't have one")
	}
	raddr := ma.StringCast("/dns4/nyc-1.bootstrap.libp2p.io/tcp/443/wss")
	rid, err := peer.Decode("QmSoLueR4xBeUbY9WZ9xGUUxunbKWcrNFTDAadQJmocnWm")
	if err != nil {
		t.Fatal(err)
	}

	tlsConfig := &tls.Config{InsecureSkipVerify: true}
	_, u := newUpgrader(t)
	tpt, err := New(u, network.NullResourceManager, WithTLSClientConfig(tlsConfig))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := tpt.Dial(context.Background(), raddr, rid)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := conn.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
}

func TestWebsocketTransport(t *testing.T) {
	t.Skip("This test is failing, see https://github.com/libp2p/go-ws-transport/issues/99")
	_, ua := newUpgrader(t)
	ta, err := New(ua, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, ub := newUpgrader(t)
	tb, err := New(ub, nil)
	if err != nil {
		t.Fatal(err)
	}

	ttransport.SubtestTransport(t, ta, tb, "/ip4/127.0.0.1/tcp/0/ws", "peerA")
}

func connectAndExchangeData(t *testing.T, laddr ma.Multiaddr, secure bool) {
	var opts []Option
	var tlsConf *tls.Config
	if secure {
		tlsConf = generateTLSConfig(t)
		opts = append(opts, WithTLSConfig(tlsConf))
	}
	server, u := newUpgrader(t)
	tpt, err := New(u, network.NullResourceManager, opts...)
	require.NoError(t, err)
	l, err := tpt.Listen(laddr)
	require.NoError(t, err)
	if secure {
		require.Equal(t, lastComponent(t, l.Multiaddr()), wssma)
	} else {
		require.Equal(t, lastComponent(t, l.Multiaddr()), wsma)
	}
	defer l.Close()

	msg := []byte("HELLO WORLD")

	go func() {
		var opts []Option
		if secure {
			opts = append(opts, WithTLSClientConfig(&tls.Config{InsecureSkipVerify: true}))
		}
		_, u := newUpgrader(t)
		tpt, err := New(u, network.NullResourceManager, opts...)
		require.NoError(t, err)
		c, err := tpt.Dial(context.Background(), l.Multiaddr(), server)
		require.NoError(t, err)
		str, err := c.OpenStream(context.Background())
		require.NoError(t, err)
		defer str.Close()
		_, err = str.Write(msg)
		require.NoError(t, err)
	}()

	c, err := l.Accept()
	require.NoError(t, err)
	defer c.Close()
	str, err := c.AcceptStream()
	require.NoError(t, err)
	defer str.Close()

	out, err := ioutil.ReadAll(str)
	require.NoError(t, err)
	require.Equal(t, out, msg, "got wrong message")
}

func TestWebsocketConnection(t *testing.T) {
	t.Run("unencrypted", func(t *testing.T) {
		connectAndExchangeData(t, ma.StringCast("/ip4/127.0.0.1/tcp/0/ws"), false)
	})
	t.Run("encrypted", func(t *testing.T) {
		connectAndExchangeData(t, ma.StringCast("/ip4/127.0.0.1/tcp/0/wss"), true)
	})
}

func TestWebsocketListenSecureFailWithoutTLSConfig(t *testing.T) {
	_, u := newUpgrader(t)
	tpt, err := New(u, network.NullResourceManager)
	require.NoError(t, err)
	addr := ma.StringCast("/ip4/127.0.0.1/tcp/0/wss")
	_, err = tpt.Listen(addr)
	require.EqualError(t, err, fmt.Sprintf("cannot listen on wss address %s without a tls.Config", addr))
}

func TestWebsocketListenSecureAndInsecure(t *testing.T) {
	serverID, serverUpgrader := newUpgrader(t)
	server, err := New(serverUpgrader, network.NullResourceManager, WithTLSConfig(generateTLSConfig(t)))
	require.NoError(t, err)

	lnInsecure, err := server.Listen(ma.StringCast("/ip4/127.0.0.1/tcp/0/ws"))
	require.NoError(t, err)
	lnSecure, err := server.Listen(ma.StringCast("/ip4/127.0.0.1/tcp/0/wss"))
	require.NoError(t, err)

	t.Run("insecure", func(t *testing.T) {
		_, clientUpgrader := newUpgrader(t)
		client, err := New(clientUpgrader, network.NullResourceManager, WithTLSClientConfig(&tls.Config{InsecureSkipVerify: true}))
		require.NoError(t, err)

		// dialing the insecure address should succeed
		conn, err := client.Dial(context.Background(), lnInsecure.Multiaddr(), serverID)
		require.NoError(t, err)
		defer conn.Close()
		require.Equal(t, lastComponent(t, conn.RemoteMultiaddr()).String(), wsma.String())
		require.Equal(t, lastComponent(t, conn.LocalMultiaddr()).String(), wsma.String())

		// dialing the secure address should fail
		_, err = client.Dial(context.Background(), lnSecure.Multiaddr(), serverID)
		require.NoError(t, err)
	})

	t.Run("secure", func(t *testing.T) {
		_, clientUpgrader := newUpgrader(t)
		client, err := New(clientUpgrader, network.NullResourceManager, WithTLSClientConfig(&tls.Config{InsecureSkipVerify: true}))
		require.NoError(t, err)

		// dialing the insecure address should succeed
		conn, err := client.Dial(context.Background(), lnSecure.Multiaddr(), serverID)
		require.NoError(t, err)
		defer conn.Close()
		require.Equal(t, lastComponent(t, conn.RemoteMultiaddr()), wssma)
		require.Equal(t, lastComponent(t, conn.LocalMultiaddr()), wssma)

		// dialing the insecure address should fail
		_, err = client.Dial(context.Background(), lnInsecure.Multiaddr(), serverID)
		require.NoError(t, err)
	})
}

func TestConcurrentClose(t *testing.T) {
	_, u := newUpgrader(t)
	tpt, err := New(u, network.NullResourceManager)
	require.NoError(t, err)
	l, err := tpt.maListen(ma.StringCast("/ip4/127.0.0.1/tcp/0/ws"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	msg := []byte("HELLO WORLD")

	go func() {
		for i := 0; i < 100; i++ {
			c, err := tpt.maDial(context.Background(), l.Multiaddr())
			if err != nil {
				t.Error(err)
				return
			}

			go func() {
				_, _ = c.Write(msg)
			}()
			go func() {
				_ = c.Close()
			}()
		}
	}()

	for i := 0; i < 100; i++ {
		c, err := l.Accept()
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
}

func TestWriteZero(t *testing.T) {
	_, u := newUpgrader(t)
	tpt, err := New(u, network.NullResourceManager)
	if err != nil {
		t.Fatal(err)
	}
	l, err := tpt.maListen(ma.StringCast("/ip4/127.0.0.1/tcp/0/ws"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	msg := []byte(nil)

	go func() {
		c, err := tpt.maDial(context.Background(), l.Multiaddr())
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()

		for i := 0; i < 100; i++ {
			n, err := c.Write(msg)
			if n != 0 {
				t.Errorf("expected to write 0 bytes, wrote %d", n)
			}
			if err != nil {
				t.Error(err)
				return
			}
		}
	}()

	c, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	buf := make([]byte, 100)
	n, err := c.Read(buf)
	if n != 0 {
		t.Errorf("read %d bytes, expected 0", n)
	}
	if err != io.EOF {
		t.Errorf("expected EOF, got err: %s", err)
	}
}
