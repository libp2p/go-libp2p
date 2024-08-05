package httppeeridauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

// TestMutualAuth tests that we can do a mutually authenticated round trip
func TestMutualAuth(t *testing.T) {
	logging.SetLogLevel("httppeeridauth", "DEBUG")

	zeroBytes := make([]byte, 64)
	serverKey, _, err := crypto.GenerateEd25519Key(bytes.NewReader(zeroBytes))
	require.NoError(t, err)

	type clientTestCase struct {
		name         string
		clientKeyGen func(t *testing.T) crypto.PrivKey
	}

	clientTestCases := []clientTestCase{
		{
			name: "ED25519",
			clientKeyGen: func(t *testing.T) crypto.PrivKey {
				t.Helper()
				clientKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
				require.NoError(t, err)
				return clientKey
			},
		},
		{
			name: "RSA",
			clientKeyGen: func(t *testing.T) crypto.PrivKey {
				t.Helper()
				clientKey, _, err := crypto.GenerateRSAKeyPair(2048, rand.Reader)
				require.NoError(t, err)
				return clientKey
			},
		},
	}

	type serverTestCase struct {
		name      string
		serverGen func(t *testing.T) (*httptest.Server, *ServerPeerIDAuth)
	}

	serverTestCases := []serverTestCase{
		{
			name: "no TLS",
			serverGen: func(t *testing.T) (*httptest.Server, *ServerPeerIDAuth) {
				t.Helper()
				auth := ServerPeerIDAuth{
					PrivKey:        serverKey,
					ValidHostnames: map[string]struct{}{"example.com": {}},
					TokenTTL:       time.Hour,
					InsecureNoTLS:  true,
				}

				ts := httptest.NewServer(&auth)
				t.Cleanup(ts.Close)
				return ts, &auth
			},
		},
		{
			name: "TLS",
			serverGen: func(t *testing.T) (*httptest.Server, *ServerPeerIDAuth) {
				t.Helper()
				auth := ServerPeerIDAuth{
					PrivKey:        serverKey,
					ValidHostnames: map[string]struct{}{"example.com": {}},
					TokenTTL:       time.Hour,
				}

				ts := httptest.NewTLSServer(&auth)
				t.Cleanup(ts.Close)
				return ts, &auth
			},
		},
	}

	for _, ctc := range clientTestCases {
		for _, stc := range serverTestCases {
			t.Run(ctc.name+"+"+stc.name, func(t *testing.T) {
				ts, serverAuth := stc.serverGen(t)
				client := ts.Client()
				tlsClientConfig := client.Transport.(*http.Transport).TLSClientConfig
				if tlsClientConfig != nil {
					// If we're using TLS, we need to set the SNI so that the
					// server can verify the request Host matches it.
					tlsClientConfig.ServerName = "example.com"
				}
				clientKey := ctc.clientKeyGen(t)
				clientAuth := ClientPeerIDAuth{PrivKey: clientKey}

				expectedServerID, err := peer.IDFromPrivateKey(serverKey)
				require.NoError(t, err)

				ctx := context.Background()
				serverID, err := clientAuth.MutualAuth(ctx, client, ts.URL, "example.com")
				require.NoError(t, err)
				require.Equal(t, expectedServerID, serverID)
				require.NotZero(t, clientAuth.tokenMap["example.com"])

				// Once more with the auth token
				req, err := http.NewRequest("GET", ts.URL, nil)
				require.NoError(t, err)
				req.Host = "example.com"
				serverID, err = clientAuth.AddAuthTokenToRequest(req)
				require.NoError(t, err)
				require.Equal(t, expectedServerID, serverID)

				// Verify that unwrapping our token gives us the client's peer ID
				expectedClientPeerID, err := peer.IDFromPrivateKey(clientKey)
				require.NoError(t, err)
				clientPeerID, err := serverAuth.UnwrapBearerToken(req, req.Host)
				require.NoError(t, err)
				require.Equal(t, expectedClientPeerID, clientPeerID)

				// Verify that we can make an authenticated request
				resp, err := client.Do(req)
				require.NoError(t, err)

				require.Equal(t, http.StatusOK, resp.StatusCode)
			})
		}
	}
}

func TestParseAuthHeader(t *testing.T) {
	testCases := []struct {
		name     string
		header   string
		expected map[string]authScheme
		err      error
	}{
		{
			name:     "empty header",
			header:   "",
			expected: nil,
			err:      nil,
		},
		{
			name:     "header too long",
			header:   strings.Repeat("a", maxAuthHeaderSize+1),
			expected: nil,
			err:      fmt.Errorf("header too long"),
		},
		{
			name:     "too many schemes",
			header:   strings.Repeat("libp2p-Bearer token1, ", maxSchemes+1),
			expected: nil,
			err:      fmt.Errorf("too many schemes"),
		},
		{
			name:     "Valid Bearer scheme",
			header:   "libp2p-Bearer token123",
			expected: map[string]authScheme{"libp2p-Bearer": {bearerToken: "token123", scheme: "libp2p-Bearer"}},
			err:      nil,
		},
		{
			name:     "Valid PeerID scheme",
			header:   "libp2p-PeerID param1=val1, param2=val2",
			expected: map[string]authScheme{"libp2p-PeerID": {scheme: "libp2p-PeerID", params: map[string]string{"param1": "val1", "param2": "val2"}}},
			err:      nil,
		},
		{
			name:   "Ignore unknown scheme",
			header: "Unknown scheme1, libp2p-Bearer token456, libp2p-PeerID param=value",
			expected: map[string]authScheme{
				"libp2p-Bearer": {
					scheme:      "libp2p-Bearer",
					bearerToken: "token456"},
				"libp2p-PeerID": {scheme: "libp2p-PeerID", params: map[string]string{"param": "value"}}},
			err: nil,
		},
		{
			name:   "Parse quoted params",
			header: `libp2p-PeerID param="value"`,
			expected: map[string]authScheme{
				"libp2p-PeerID": {scheme: "libp2p-PeerID", params: map[string]string{"param": "value"}}},
			err: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual, err := parseAuthHeader(tc.header)
			if tc.err != nil {
				require.Error(t, err, tc.err)
				require.Equal(t, tc.err.Error(), err.Error())
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expected, actual)
			}
		})
	}
}

func FuzzParseAuthHeader(f *testing.F) {
	// Just check that we don't panic'
	f.Fuzz(func(t *testing.T, data []byte) {
		parseAuthHeader(string(data))
	})
}

func FuzzServeHTTP(f *testing.F) {
	zeroBytes := make([]byte, 64)
	serverKey, _, err := crypto.GenerateEd25519Key(bytes.NewReader(zeroBytes))
	require.NoError(f, err)
	auth := ServerPeerIDAuth{
		PrivKey:        serverKey,
		ValidHostnames: map[string]struct{}{"example.com": {}},
		TokenTTL:       time.Hour,
		InsecureNoTLS:  true,
	}
	// Just check that we don't panic'
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		hostLen := int(data[0])
		data = data[1:]
		if hostLen > len(data) {
			return
		}
		host := string(data[:hostLen])
		data = data[hostLen:]
		req := httptest.NewRequest("GET", "http://example.com", nil)
		req.Host = host
		req.Header.Set("Authorization", string(data))
		auth.ServeHTTP(httptest.NewRecorder(), req)
	})
}

func BenchmarkAuths(b *testing.B) {
	zeroBytes := make([]byte, 64)
	serverKey, _, err := crypto.GenerateEd25519Key(bytes.NewReader(zeroBytes))
	require.NoError(b, err)
	auth := ServerPeerIDAuth{
		PrivKey:        serverKey,
		ValidHostnames: map[string]struct{}{"example.com": {}},
		TokenTTL:       time.Hour,
		InsecureNoTLS:  true,
	}

	ts := httptest.NewServer(&auth)
	defer ts.Close()

	ctx := context.Background()
	client := &http.Client{}
	clientKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
	require.NoError(b, err)
	clientAuth := ClientPeerIDAuth{PrivKey: clientKey}
	clientID, err := peer.IDFromPrivateKey(clientKey)
	require.NoError(b, err)
	challengeServer := make([]byte, challengeLen)
	clientAuthValue, err := clientAuth.authSelfToServer(ctx, client, clientID, challengeServer, ts.URL, "example.com")
	require.NoError(b, err)

	b.ResetTimer()
	req, err := http.NewRequest("GET", ts.URL, nil)
	require.NoError(b, err)
	req.Host = "example.com"
	req.Header.Set("Authorization", clientAuthValue)

	for i := 0; i < b.N; i++ {
		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			b.Fatal(err, resp.StatusCode)
		}
	}
}

// Test Vectors
var zeroBytes = make([]byte, 64)
var zeroKey, _, _ = crypto.GenerateEd25519Key(bytes.NewReader(zeroBytes))

// Peer ID derived from the zero key
var zeroID, _ = peer.IDFromPublicKey(zeroKey.GetPublic())

func genClientID(t *testing.T) (peer.ID, crypto.PrivKey) {
	clientPrivStr, err := hex.DecodeString("080112407e0830617c4a7de83925dfb2694556b12936c477a0e1feb2e148ec9da60fee7d1ed1e8fae2c4a144b8be8fd4b47bf3d3b34b871c3cacf6010f0e42d474fce27e")
	require.NoError(t, err)
	clientKey, err := crypto.UnmarshalPrivateKey(clientPrivStr)
	require.NoError(t, err)
	clientID, err := peer.IDFromPrivateKey(clientKey)
	require.NoError(t, err)
	return clientID, clientKey
}

// TestWalkthroughInSpec tests the walkthrough example in libp2p/specs
func TestWalkthroughInSpec(t *testing.T) {
	marshalledZeroKey, err := crypto.MarshalPrivateKey(zeroKey)
	require.NoError(t, err)
	// To demonstrate the marshalled version of the zero key. In js-libp2p (maybe others?) it's easier to consume this form.
	require.Equal(t, "0801124000000000000000000000000000000000000000000000000000000000000000003b6a27bcceb6a42d62a3a8d02a6f0d73653215771de243a63ac048a18b59da29", hex.EncodeToString(marshalledZeroKey))

	zeroBytes := make([]byte, 32)
	clientID, clientKey := genClientID(t)
	require.Equal(t, "12D3KooWBtg3aaRMjxwedh83aGiUkwSxDwUZkzuJcfaqUmo7R3pq", clientID.String())

	challengeClientb64 := base64.URLEncoding.EncodeToString(zeroBytes)
	require.Equal(t, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", challengeClientb64)
	challengeServer64 := "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="

	hostname := "example.com"

	clientParts := []string{
		"challenge-client=" + challengeClientb64,
		fmt.Sprintf(`hostname="%s"`, hostname),
	}
	toSign, err := genDataToSign(nil, PeerIDAuthScheme, clientParts)
	require.NoError(t, err)
	require.Equal(t, "libp2p-PeerID=challenge-client=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=%16hostname=%22example.com%22", url.PathEscape(string(toSign)))
	sig, err := sign(clientKey, PeerIDAuthScheme, clientParts)
	require.NoError(t, err)
	require.Equal(t, "F5OBYbbMXoIVJNWrW0UANi7rrbj4GCB6kcEceQjajLTMvC-_jpBF9MFlxiaNYXOEiPQqeo_S56YUSNinwl0ZCQ==", base64.URLEncoding.EncodeToString(sig))

	serverID := zeroID
	require.Equal(t, "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN", serverID.String())

	serverParts := []string{
		"challenge-server=" + challengeServer64,
		"client=" + clientID.String(),
		fmt.Sprintf(`hostname="%s"`, hostname),
	}
	toSign, err = genDataToSign(nil, PeerIDAuthScheme, serverParts)
	require.NoError(t, err)
	require.Equal(t, "libp2p-PeerID=challenge-server=BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=%3Bclient=12D3KooWBtg3aaRMjxwedh83aGiUkwSxDwUZkzuJcfaqUmo7R3pq%16hostname=%22example.com%22", url.PathEscape(string(toSign)))

	sig, err = sign(zeroKey, PeerIDAuthScheme, serverParts)
	require.NoError(t, err)
	require.Equal(t, "btLFqW200aDTQqpkKetJJje7V-iDknXygFqPsfiegNsboXeYDiQ6Rqcpezz1wfr8j9h83QkN9z78cAWzKzV_AQ==", base64.URLEncoding.EncodeToString(sig))
}
