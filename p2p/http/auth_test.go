package libp2phttp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"
	"github.com/multiformats/go-multiaddr"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/stretchr/testify/require"
)

func TestHappyPathIX(t *testing.T) {
	req, err := http.NewRequest("GET", "http://example.com", nil)
	require.NoError(t, err)
	respHeader := make(http.Header)

	clientKey, _, err := test.RandTestKeyPair(crypto.Ed25519, 256)
	require.NoError(t, err)
	serverKey, _, err := test.RandTestKeyPair(crypto.Ed25519, 256)
	require.NoError(t, err)

	// Client starts a request
	authState, err := WithNoiseAuthentication(clientKey, req.Header)
	require.NoError(t, err)

	// Server responds to the request and sets the appropriate headers
	clientPeerID, err := AuthenticateClient(serverKey, respHeader, req)
	require.NoError(t, err)
	require.NotEmpty(t, clientPeerID)
	require.NotEmpty(t, respHeader.Get("Authentication-Info"))

	expectedClientKey, err := peer.IDFromPrivateKey(clientKey)
	require.NoError(t, err)
	require.Equal(t, expectedClientKey, clientPeerID)

	// Client receives the response and validates the auth info
	serverPeerID, err := authState.AuthenticateServer("", respHeader)
	require.NoError(t, err)

	expectedServerID, err := peer.IDFromPrivateKey(serverKey)
	require.NoError(t, err)
	require.Equal(t, expectedServerID, serverPeerID)
}

func TestServerHandlerRequiresAuth(t *testing.T) {
	clientKey, _, err := test.RandTestKeyPair(crypto.Ed25519, 256)
	require.NoError(t, err)
	serverKey, _, err := test.RandTestKeyPair(crypto.Ed25519, 256)
	require.NoError(t, err)

	// Start the server
	server, err := New()
	require.NoError(t, err)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()

	server.SetHttpHandlerAtPath("/my-app/1", "/my-app/1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This handler requires libp2p auth
		clientID, err := AuthenticateClient(serverKey, w.Header(), r)

		if err != nil || clientID == "" {
			w.Header().Set("WWW-Authenticate", "Libp2p-Noise-IX")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf("Hello, %s", clientID)))
	}))

	// Middleware example
	type Libp2pAuthKey struct{}
	var libp2pAuthKey = Libp2pAuthKey{}
	libp2pAuthMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// This middleware requires libp2p auth
			clientID, err := AuthenticateClient(serverKey, w.Header(), r)

			if err != nil || clientID == "" {
				w.Header().Set("WWW-Authenticate", "Libp2p-Noise-IX")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			r = r.WithContext(context.WithValue(r.Context(), libp2pAuthKey, clientID))
			next.ServeHTTP(w, r)
		})
	}

	server.SetHttpHandler("/my-app-middleware/1", libp2pAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf("Hello, %s", r.Context().Value(libp2pAuthKey))))
	})))

	go server.Serve(l)

	client := http.Client{}

	pathsToTry := []string{
		fmt.Sprintf("http://%s/my-app/1", l.Addr().String()),
		fmt.Sprintf("http://%s/my-app-middleware/1", l.Addr().String()),
	}

	for _, path := range pathsToTry {
		// Try without auth
		resp, err := client.Get(path)
		require.NoError(t, err)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

		// Try with auth
		req, err := http.NewRequest("GET", path, nil)
		require.NoError(t, err)

		// Set the Authorization header
		authState, err := WithNoiseAuthentication(clientKey, req.Header)
		require.NoError(t, err)

		// Make the request
		resp, err = client.Do(req)
		require.NoError(t, err)

		require.Equal(t, http.StatusOK, resp.StatusCode)
		respBody, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		resp.Body.Close()

		expectedClientKey, err := peer.IDFromPrivateKey(clientKey)
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("Hello, %s", expectedClientKey), string(respBody))

		// Client receives the response and validates the auth info
		serverPeerID, err := authState.AuthenticateServer("", resp.Header)
		require.NoError(t, err)

		expectedServerID, err := peer.IDFromPrivateKey(serverKey)
		require.NoError(t, err)
		require.Equal(t, expectedServerID, serverPeerID)
	}
}

func TestSNIIsUsed(t *testing.T) {
	serverKey, _, err := test.RandTestKeyPair(crypto.Ed25519, 256)
	require.NoError(t, err)
	serverID, err := peer.IDFromPrivateKey(serverKey)
	require.NoError(t, err)

	serverHTTPHost, err := New()
	require.NoError(t, err)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	serverHTTPHost.SetHttpHandler(AuthHandlerProtocolID, &AuthHandler{serverKey})
	require.NoError(t, err)
	defer l.Close()
	tlsConf := getTLSConf(t, net.IPv4(127, 0, 0, 1), time.Now(), time.Now().Add(time.Hour), "example.com")
	go serverHTTPHost.ServeTLS(l, tlsConf)

	serverMaSNI, err := manet.FromNetAddr(l.Addr())
	require.NoError(t, err)
	serverMaWrongSNI := ma.Join(serverMaSNI, multiaddr.StringCast("/tls/sni/wrong.com/http"))
	serverMaSNI = ma.Join(serverMaSNI, multiaddr.StringCast("/tls/sni/example.com/http"))
	serverMaDNS := multiaddr.StringCast("/dns4/example.com/tcp/443/tls/http")

	testCases := []struct {
		name       string
		ma         ma.Multiaddr
		shouldFail bool
	}{
		{
			name:       "With sni: " + serverMaSNI.String(),
			ma:         serverMaSNI,
			shouldFail: false,
		},
		{
			name:       "With wrong sni: " + serverMaWrongSNI.String(),
			ma:         serverMaWrongSNI,
			shouldFail: true,
		},
		{
			name:       "With dns: " + serverMaDNS.String(),
			ma:         serverMaDNS,
			shouldFail: false,
		},
	}

	for _, tc := range testCases {
		t.Run("With multiaddr "+tc.name, func(t *testing.T) {
			clientHttpHost, err := New(WithTLSClientConfig(&tls.Config{InsecureSkipVerify: true}))
			require.NoError(t, err)

			tr := clientHttpHost.httpRoundTripper.Clone()
			// Resolve example.com to the server's IP in our test
			clientHttpHost.httpRoundTripper.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				// Returns our listener's addr
				return tr.DialContext(ctx, l.Addr().Network(), l.Addr().String())
			}
			client, err := clientHttpHost.NamespacedClient(nil, AuthHandlerProtocolID, peer.AddrInfo{ID: serverID, Addrs: []multiaddr.Multiaddr{tc.ma}})
			if tc.shouldFail {
				require.Error(t, err)
			} else {

				require.NoError(t, err)
			}

			clientKey, _, err := test.RandTestKeyPair(crypto.Ed25519, 256)
			require.NoError(t, err)

			observedServerID, err := DoAuth(client, clientKey)
			if tc.shouldFail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, serverID, observedServerID)
			}
		})
	}
}

func getTLSConf(t *testing.T, ip net.IP, start, end time.Time, expectSNI string) *tls.Config {
	t.Helper()
	certTempl := &x509.Certificate{
		DNSNames:              []string{expectSNI},
		SerialNumber:          big.NewInt(1234),
		Subject:               pkix.Name{Organization: []string{"https-test"}},
		NotBefore:             start,
		NotAfter:              end,
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caBytes, err := x509.CreateCertificate(rand.Reader, certTempl, certTempl, &priv.PublicKey, priv)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(caBytes)
	require.NoError(t, err)
	var c *tls.Config
	c = &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{cert.Raw},
			PrivateKey:  priv,
			Leaf:        cert,
		}},
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			if hello.ServerName != expectSNI {
				return nil, errors.New("unexpected SNI")
			}
			return c, nil
		},
	}
	return c
}
