package websocket

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/core/sec/insecure"
	"github.com/libp2p/go-libp2p/core/test"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	tptu "github.com/libp2p/go-libp2p/p2p/net/upgrader"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	ttransport "github.com/libp2p/go-libp2p/p2p/transport/testsuite"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func newUpgrader(t *testing.T) (peer.ID, transport.Upgrader) {
	t.Helper()
	id, m := newInsecureMuxer(t)
	u, err := tptu.New(m, []tptu.StreamMuxer{{ID: "/yamux", Muxer: yamux.DefaultTransport}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return id, u
}

func newSecureUpgrader(t *testing.T) (peer.ID, transport.Upgrader) {
	t.Helper()
	id, m := newSecureMuxer(t)
	u, err := tptu.New(m, []tptu.StreamMuxer{{ID: "/yamux", Muxer: yamux.DefaultTransport}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return id, u
}

func newInsecureMuxer(t *testing.T) (peer.ID, []sec.SecureTransport) {
	t.Helper()
	priv, _, err := test.RandTestKeyPair(crypto.Ed25519, 256)
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(priv)
	require.NoError(t, err)
	return id, []sec.SecureTransport{insecure.NewWithIdentity(insecure.ID, id, priv)}
}

func newSecureMuxer(t *testing.T) (peer.ID, []sec.SecureTransport) {
	t.Helper()
	priv, _, err := test.RandTestKeyPair(crypto.Ed25519, 256)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	noiseTpt, err := noise.New(noise.ID, priv, nil)
	require.NoError(t, err)
	return id, []sec.SecureTransport{noiseTpt}
}

func lastComponent(t *testing.T, a ma.Multiaddr) ma.Multiaddr {
	t.Helper()
	_, wscomponent := ma.SplitLast(a)
	require.NotNil(t, wscomponent)
	if wscomponent.Equal(wsComponent) {
		return wsComponent
	}
	if wscomponent.Equal(wssComponent) {
		return wssComponent
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
	if !d.CanDial(ma.StringCast("/ip4/127.0.0.1/tcp/5555/tls/ws")) {
		t.Fatal("expected to match secure websocket maddr, but did not")
	}
	if !d.CanDial(ma.StringCast("/ip4/127.0.0.1/tcp/5555/tls/sni/example.com/ws")) {
		t.Fatal("expected to match secure websocket maddr with sni, but did not")
	}
	if !d.CanDial(ma.StringCast("/dns4/example.com/tcp/5555/tls/sni/example.com/ws")) {
		t.Fatal("expected to match secure websocket maddr with sni, but did not")
	}
	if !d.CanDial(ma.StringCast("/dnsaddr/example.com/tcp/5555/tls/sni/example.com/ws")) {
		t.Fatal("expected to match secure websocket maddr with sni, but did not")
	}
}

// testWSSServer returns a client hello info
func testWSSServer(t *testing.T, listenAddr ma.Multiaddr) (ma.Multiaddr, peer.ID, chan error) {
	errChan := make(chan error, 1)

	ip := net.ParseIP("::")
	tlsConf := getTLSConf(t, ip, time.Now(), time.Now().Add(time.Hour))
	tlsConf.GetConfigForClient = func(chi *tls.ClientHelloInfo) (*tls.Config, error) {
		if chi.ServerName != "example.com" {
			errChan <- fmt.Errorf("didn't get the expected sni")
		}
		return tlsConf, nil
	}

	id, u := newSecureUpgrader(t)
	tpt, err := New(u, &network.NullResourceManager{}, nil, WithTLSConfig(tlsConf))
	if err != nil {
		t.Fatal(err)
	}

	l, err := tpt.Listen(listenAddr)
	require.NoError(t, err)
	t.Cleanup(func() {
		l.Close()
	})
	go func() {
		conn, err := l.Accept()
		if err != nil {
			errChan <- fmt.Errorf("error in accepting conn: %w", err)
			return
		}
		defer conn.Close()

		strm, err := conn.AcceptStream()
		if err != nil {
			errChan <- fmt.Errorf("error in accepting stream: %w", err)
			return
		}
		defer strm.Close()
		close(errChan)
	}()

	return l.Multiaddr(), id, errChan
}

func getTLSConf(t *testing.T, ip net.IP, start, end time.Time) *tls.Config {
	t.Helper()
	certTempl := &x509.Certificate{
		SerialNumber:          big.NewInt(1234),
		Subject:               pkix.Name{Organization: []string{"websocket"}},
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

func TestHostHeaderWss(t *testing.T) {
	server := &http.Server{}
	l, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	defer server.Close()

	errChan := make(chan error, 1)
	go func() {
		server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer close(errChan)
			if !strings.Contains(r.Host, "example.com") {
				errChan <- errors.New("Didn't see host header")
			}
			w.WriteHeader(http.StatusNotFound)
		})
		server.TLSConfig = getTLSConf(t, net.ParseIP("127.0.0.1"), time.Now(), time.Now().Add(time.Hour))
		server.ServeTLS(l, "", "")
	}()

	_, port, err := net.SplitHostPort(l.Addr().String())
	require.NoError(t, err)
	serverMA := ma.StringCast("/ip4/127.0.0.1/tcp/" + port + "/tls/sni/example.com/ws")

	tlsConfig := &tls.Config{InsecureSkipVerify: true} // Our test server doesn't have a cert signed by a CA
	_, u := newSecureUpgrader(t)
	tpt, err := New(u, &network.NullResourceManager{}, nil, WithTLSClientConfig(tlsConfig))
	require.NoError(t, err)

	masToDial, err := tpt.Resolve(context.Background(), serverMA)
	require.NoError(t, err)

	_, err = tpt.Dial(context.Background(), masToDial[0], test.RandPeerIDFatal(t))
	require.Error(t, err)

	err = <-errChan
	require.NoError(t, err)
}

func TestDialWss(t *testing.T) {
	serverMA, rid, errChan := testWSSServer(t, ma.StringCast("/ip4/127.0.0.1/tcp/0/tls/sni/example.com/ws"))
	require.Contains(t, serverMA.String(), "tls")

	tlsConfig := &tls.Config{InsecureSkipVerify: true} // Our test server doesn't have a cert signed by a CA
	_, u := newSecureUpgrader(t)
	tpt, err := New(u, &network.NullResourceManager{}, nil, WithTLSClientConfig(tlsConfig))
	require.NoError(t, err)

	masToDial, err := tpt.Resolve(context.Background(), serverMA)
	require.NoError(t, err)

	conn, err := tpt.Dial(context.Background(), masToDial[0], rid)
	require.NoError(t, err)
	defer conn.Close()

	stream, err := conn.OpenStream(context.Background())
	require.NoError(t, err)
	defer stream.Close()

	err = <-errChan
	require.NoError(t, err)
}

func TestDialWssNoClientCert(t *testing.T) {
	serverMA, rid, _ := testWSSServer(t, ma.StringCast("/ip4/127.0.0.1/tcp/0/tls/sni/example.com/ws"))
	require.Contains(t, serverMA.String(), "tls")

	_, u := newSecureUpgrader(t)
	tpt, err := New(u, &network.NullResourceManager{}, nil)
	require.NoError(t, err)

	masToDial, err := tpt.Resolve(context.Background(), serverMA)
	require.NoError(t, err)

	_, err = tpt.Dial(context.Background(), masToDial[0], rid)
	require.Error(t, err)

	// The server doesn't have a signed certificate
	require.Contains(t, err.Error(), "x509")
}

func TestWebsocketTransport(t *testing.T) {
	peerA, ua := newUpgrader(t)
	ta, err := New(ua, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, ub := newUpgrader(t)
	tb, err := New(ub, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	ttransport.SubtestTransport(t, ta, tb, "/ip4/127.0.0.1/tcp/0/ws", peerA)
}

func isWSS(addr ma.Multiaddr) bool {
	if _, err := addr.ValueForProtocol(ma.P_WSS); err == nil {
		return true
	}
	if _, err := addr.ValueForProtocol(ma.P_WS); err == nil {
		return false
	}
	panic("not a WebSocket address")
}

func connectAndExchangeData(t *testing.T, laddr ma.Multiaddr, secure bool) {
	var opts []Option
	var tlsConf *tls.Config
	if secure {
		tlsConf = generateTLSConfig(t)
		opts = append(opts, WithTLSConfig(tlsConf))
	}
	server, u := newUpgrader(t)
	tpt, err := New(u, &network.NullResourceManager{}, nil, opts...)
	require.NoError(t, err)
	l, err := tpt.Listen(laddr)
	require.NoError(t, err)
	if secure {
		require.Contains(t, l.Multiaddr().String(), "tls")
	} else {
		require.Equal(t, lastComponent(t, l.Multiaddr()), wsComponent)
	}
	defer l.Close()

	msg := []byte("HELLO WORLD")

	go func() {
		var opts []Option
		if secure {
			opts = append(opts, WithTLSClientConfig(&tls.Config{InsecureSkipVerify: true}))
		}
		_, u := newUpgrader(t)
		tpt, err := New(u, &network.NullResourceManager{}, nil, opts...)
		require.NoError(t, err)
		c, err := tpt.Dial(context.Background(), l.Multiaddr(), server)
		require.NoError(t, err)
		require.Equal(t, secure, isWSS(c.LocalMultiaddr()))
		require.Equal(t, secure, isWSS(c.RemoteMultiaddr()))
		str, err := c.OpenStream(context.Background())
		require.NoError(t, err)
		defer str.Close()
		_, err = str.Write(msg)
		require.NoError(t, err)
	}()

	c, err := l.Accept()
	require.NoError(t, err)
	defer c.Close()
	require.Equal(t, secure, isWSS(c.LocalMultiaddr()))
	require.Equal(t, secure, isWSS(c.RemoteMultiaddr()))
	str, err := c.AcceptStream()
	require.NoError(t, err)
	defer str.Close()

	out, err := io.ReadAll(str)
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
	tpt, err := New(u, &network.NullResourceManager{}, nil)
	require.NoError(t, err)
	addr := ma.StringCast("/ip4/127.0.0.1/tcp/0/wss")
	_, err = tpt.Listen(addr)
	require.EqualError(t, err, fmt.Sprintf("cannot listen on wss address %s without a tls.Config", addr))
}

func TestWebsocketListenSecureAndInsecure(t *testing.T) {
	serverID, serverUpgrader := newUpgrader(t)
	server, err := New(serverUpgrader, &network.NullResourceManager{}, nil, WithTLSConfig(generateTLSConfig(t)))
	require.NoError(t, err)

	lnInsecure, err := server.Listen(ma.StringCast("/ip4/127.0.0.1/tcp/0/ws"))
	require.NoError(t, err)
	lnSecure, err := server.Listen(ma.StringCast("/ip4/127.0.0.1/tcp/0/wss"))
	require.NoError(t, err)

	t.Run("insecure", func(t *testing.T) {
		_, clientUpgrader := newUpgrader(t)
		client, err := New(clientUpgrader, &network.NullResourceManager{}, nil, WithTLSClientConfig(&tls.Config{InsecureSkipVerify: true}))
		require.NoError(t, err)

		// dialing the insecure address should succeed
		conn, err := client.Dial(context.Background(), lnInsecure.Multiaddr(), serverID)
		require.NoError(t, err)
		defer conn.Close()
		require.Equal(t, lastComponent(t, conn.RemoteMultiaddr()).String(), wsComponent.String())
		require.Equal(t, lastComponent(t, conn.LocalMultiaddr()).String(), wsComponent.String())

		// dialing the secure address should fail
		_, err = client.Dial(context.Background(), lnSecure.Multiaddr(), serverID)
		require.NoError(t, err)
	})

	t.Run("secure", func(t *testing.T) {
		_, clientUpgrader := newUpgrader(t)
		client, err := New(clientUpgrader, &network.NullResourceManager{}, nil, WithTLSClientConfig(&tls.Config{InsecureSkipVerify: true}))
		require.NoError(t, err)

		// dialing the insecure address should succeed
		conn, err := client.Dial(context.Background(), lnSecure.Multiaddr(), serverID)
		require.NoError(t, err)
		defer conn.Close()
		require.Equal(t, lastComponent(t, conn.RemoteMultiaddr()), wssComponent)
		require.Equal(t, lastComponent(t, conn.LocalMultiaddr()), wssComponent)

		// dialing the insecure address should fail
		_, err = client.Dial(context.Background(), lnInsecure.Multiaddr(), serverID)
		require.NoError(t, err)
	})
}

func TestConcurrentClose(t *testing.T) {
	_, u := newUpgrader(t)
	tpt, err := New(u, &network.NullResourceManager{}, nil)
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
	tpt, err := New(u, &network.NullResourceManager{}, nil)
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

func TestResolveMultiaddr(t *testing.T) {
	// map[unresolved]resolved
	testCases := map[string]string{
		"/dns/example.com/tcp/1234/wss":        "/dns/example.com/tcp/1234/tls/sni/example.com/ws",
		"/dns4/example.com/tcp/1234/wss":       "/dns4/example.com/tcp/1234/tls/sni/example.com/ws",
		"/dns6/example.com/tcp/1234/wss":       "/dns6/example.com/tcp/1234/tls/sni/example.com/ws",
		"/dnsaddr/example.com/tcp/1234/wss":    "/dnsaddr/example.com/tcp/1234/wss",
		"/dns4/example.com/tcp/1234/tls/ws":    "/dns4/example.com/tcp/1234/tls/sni/example.com/ws",
		"/dns6/example.com/tcp/1234/tls/ws":    "/dns6/example.com/tcp/1234/tls/sni/example.com/ws",
		"/dnsaddr/example.com/tcp/1234/tls/ws": "/dnsaddr/example.com/tcp/1234/tls/ws",
	}

	for unresolved, expectedMA := range testCases {
		t.Run(unresolved, func(t *testing.T) {

			m1 := ma.StringCast(unresolved)
			wsTpt := WebsocketTransport{}
			ctx := context.Background()

			addrs, err := wsTpt.Resolve(ctx, m1)
			require.NoError(t, err)
			require.Len(t, addrs, 1)

			require.Equal(t, expectedMA, addrs[0].String())
		})
	}
}
