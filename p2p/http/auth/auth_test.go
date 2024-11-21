package httppeeridauth

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"hash"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/assert"
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
					PrivKey: serverKey,
					ValidHostnameFn: func(s string) bool {
						return s == "example.com"
					},
					TokenTTL: time.Hour,
					NoTLS:    true,
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
					PrivKey: serverKey,
					ValidHostnameFn: func(s string) bool {
						return s == "example.com"
					},
					TokenTTL: time.Hour,
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
				ts, server := stc.serverGen(t)
				client := ts.Client()
				roundTripper := instrumentedRoundTripper{client.Transport, 0}
				client.Transport = &roundTripper
				requestsSent := func() int {
					defer func() { roundTripper.timesRoundtripped = 0 }()
					return roundTripper.timesRoundtripped
				}

				tlsClientConfig := roundTripper.TLSClientConfig()
				if tlsClientConfig != nil {
					// If we're using TLS, we need to set the SNI so that the
					// server can verify the request Host matches it.
					tlsClientConfig.ServerName = "example.com"
				}
				clientKey := ctc.clientKeyGen(t)
				clientAuth := ClientPeerIDAuth{PrivKey: clientKey}

				expectedServerID, err := peer.IDFromPrivateKey(serverKey)
				require.NoError(t, err)

				req, err := http.NewRequest("POST", ts.URL, nil)
				require.NoError(t, err)
				req.Host = "example.com"
				serverID, resp, err := clientAuth.AuthenticatedDo(client, req)
				require.NoError(t, err)
				require.Equal(t, expectedServerID, serverID)
				require.NotZero(t, clientAuth.tm.tokenMap["example.com"])
				require.Equal(t, http.StatusOK, resp.StatusCode)
				require.Equal(t, 2, requestsSent())

				// Once more with the auth token
				req, err = http.NewRequest("POST", ts.URL, nil)
				require.NoError(t, err)
				req.Host = "example.com"
				serverID, resp, err = clientAuth.AuthenticatedDo(client, req)
				require.NotEmpty(t, req.Header.Get("Authorization"))
				require.True(t, HasAuthHeader(req))
				require.NoError(t, err)
				require.Equal(t, expectedServerID, serverID)
				require.NotZero(t, clientAuth.tm.tokenMap["example.com"])
				require.Equal(t, http.StatusOK, resp.StatusCode)
				require.Equal(t, 1, requestsSent(), "should only call newRequest once since we have a token")

				t.Run("Tokens Expired", func(t *testing.T) {
					// Clear the auth token on the server side
					server.TokenTTL = 1 // Small TTL
					time.Sleep(100 * time.Millisecond)
					resetServerTokenTTL := sync.OnceFunc(func() {
						server.TokenTTL = time.Hour
					})

					req, err := http.NewRequest("POST", ts.URL, nil)
					require.NoError(t, err)
					req.Host = "example.com"
					req.GetBody = func() (io.ReadCloser, error) {
						resetServerTokenTTL()
						return nil, nil
					}
					serverID, resp, err = clientAuth.AuthenticatedDo(client, req)
					require.NoError(t, err)
					require.NotEmpty(t, req.Header.Get("Authorization"))
					require.Equal(t, http.StatusOK, resp.StatusCode)
					require.Equal(t, expectedServerID, serverID)
					require.NotZero(t, clientAuth.tm.tokenMap["example.com"])
					require.Equal(t, 3, requestsSent(), "should call newRequest 3x since our token expired")
				})

				t.Run("Tokens Invalidated", func(t *testing.T) {
					// Clear the auth token on the server side
					server.Hmac = func() hash.Hash {
						key := make([]byte, 32)
						_, err := rand.Read(key)
						if err != nil {
							panic(err)
						}
						return hmac.New(sha256.New, key)
					}()

					req, err := http.NewRequest("POST", ts.URL, nil)
					req.GetBody = func() (io.ReadCloser, error) {
						return nil, nil
					}
					require.NoError(t, err)
					req.Host = "example.com"
					serverID, resp, err = clientAuth.AuthenticatedDo(client, req)
					require.NoError(t, err)
					require.NotEmpty(t, req.Header.Get("Authorization"))
					require.Equal(t, http.StatusOK, resp.StatusCode)
					require.Equal(t, expectedServerID, serverID)
					require.NotZero(t, clientAuth.tm.tokenMap["example.com"])
					require.Equal(t, 3, requestsSent(), "should call have sent 3 reqs since our token expired")
				})

			})
		}
	}
}

func TestBodyNotSentDuringRedirect(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.Empty(t, string(b))
		if r.URL.Path != "/redirected" {
			w.Header().Set("Location", "/redirected")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
	}))
	t.Cleanup(ts.Close)
	client := ts.Client()
	clientKey, _, _ := crypto.GenerateEd25519Key(rand.Reader)
	clientAuth := ClientPeerIDAuth{PrivKey: clientKey}

	req, err :=
		http.NewRequest(
			"POST",
			ts.URL,
			strings.NewReader("Only for authenticated servers"),
		)
	req.Host = "example.com"
	require.NoError(t, err)
	_, _, err = clientAuth.AuthenticatedDo(client, req)
	require.ErrorContains(t, err, "signature not set") // server doesn't actually handshake
}

type instrumentedRoundTripper struct {
	http.RoundTripper
	timesRoundtripped int
}

func (irt *instrumentedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	irt.timesRoundtripped++
	return irt.RoundTripper.RoundTrip(req)
}

func (irt *instrumentedRoundTripper) TLSClientConfig() *tls.Config {
	return irt.RoundTripper.(*http.Transport).TLSClientConfig
}
