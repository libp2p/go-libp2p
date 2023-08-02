package libp2phttp

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"
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
	server := New()
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

// TODO test with TLS + sni
