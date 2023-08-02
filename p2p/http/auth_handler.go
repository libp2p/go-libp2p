package libp2phttp

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

const AuthHandlerProtocolID = "/http-noise-auth/1.0.0"

type AuthHandler struct {
	hostKey crypto.PrivKey
}

var _ http.Handler = (*AuthHandler)(nil)

// ServeHTTP implements http.Handler.
func (h *AuthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	clientID, err := AuthenticateClient(h.hostKey, w.Header(), r)

	if err != nil || clientID == "" {
		w.Header().Set("WWW-Authenticate", "Libp2p-Noise-IX")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// DoAuth sends an auth request over HTTP. The provided client should be
// namespaced to the AuthHandler protocol.
//
// Returns the server's peer id.
func DoAuth(client http.Client, clientKey crypto.PrivKey) (peer.ID, error) {
	req, err := http.NewRequest("POST", "/", nil)
	if err != nil {
		return "", err
	}
	a, err := WithNoiseAuthentication(clientKey, req.Header)
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	if resp.TLS == nil {
		return "", errors.New("expected TLS connection")
	}

	expectedHost := resp.TLS.ServerName
	return a.AuthenticateServer(expectedHost, resp.Header)
}
