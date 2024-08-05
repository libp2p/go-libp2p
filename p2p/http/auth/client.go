package httppeeridauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

type ClientPeerIDAuth struct {
	PrivKey    crypto.PrivKey
	tokenMapMu sync.Mutex
	tokenMap   map[string]tokenInfo
}

type tokenInfo struct {
	peerID peer.ID
	token  string
}

var ErrNoAuthToken = errors.New("no auth token found")

// AddAuthTokenToRequest adds the libp2p-Bearer token to the request. Returns the peer ID of the server.
func (a *ClientPeerIDAuth) AddAuthTokenToRequest(req *http.Request) (peer.ID, error) {
	a.tokenMapMu.Lock()
	defer a.tokenMapMu.Unlock()
	if a.tokenMap == nil {
		a.tokenMap = make(map[string]tokenInfo)
	}

	t, ok := a.tokenMap[req.Host]
	if !ok {
		return "", ErrNoAuthToken
	}

	req.Header.Set("Authorization", BearerAuthScheme+" "+t.token)
	return t.peerID, nil
}

// MutualAuth performs mutual authentication with the server at the given endpoint. Returns the server's peer id.
func (a *ClientPeerIDAuth) MutualAuth(ctx context.Context, client *http.Client, authEndpoint string, hostname string) (peer.ID, error) {
	if a.PrivKey == nil {
		return "", errors.New("no private key set")
	}

	myPeerID, err := peer.IDFromPrivateKey(a.PrivKey)
	if err != nil {
		return "", fmt.Errorf("failed to get peer ID: %w", err)
	}

	var challengeServer [challengeLen]byte
	_, err = rand.Read(challengeServer[:])
	if err != nil {
		return "", fmt.Errorf("failed to generate challenge-server: %w", err)
	}
	authValue, err := a.authSelfToServer(ctx, client, myPeerID, challengeServer[:], authEndpoint, hostname)
	if err != nil {
		return "", fmt.Errorf("failed to authenticate self to server: %w", err)
	}

	authServerReq, err := http.NewRequestWithContext(ctx, "GET", authEndpoint, nil)
	authServerReq.Host = hostname
	if err != nil {
		return "", fmt.Errorf("failed to create request to authenticate server: %w", err)
	}
	authServerReq.Header.Set("Authorization", authValue)
	resp, err := client.Do(authServerReq)
	if err != nil {
		return "", fmt.Errorf("failed to do authenticate server request: %w", err)
	}
	resp.Body.Close()

	// Verify the server's signature
	respAuth, err := parseAuthFields(resp.Header.Get("Authentication-Info"), hostname, false)
	if err != nil {
		return "", fmt.Errorf("failed to parse Authentication-Info header: %w", err)
	}
	serverID, err := a.verifySigFromServer(respAuth, myPeerID, challengeServer[:])
	if err != nil {
		return "", fmt.Errorf("failed to authenticate server: %w", err)
	}

	// Auth succeeded, store the token
	respAuthSchemes, err := parseAuthHeader(resp.Header.Get("Authorization"))
	if err != nil {
		return "", fmt.Errorf("failed to parse auth header: %w", err)
	}

	if bearer, ok := respAuthSchemes[BearerAuthScheme]; ok {
		a.tokenMapMu.Lock()
		if a.tokenMap == nil {
			a.tokenMap = make(map[string]tokenInfo)
		}
		a.tokenMap[hostname] = tokenInfo{token: bearer.bearerToken, peerID: serverID}
		a.tokenMapMu.Unlock()
	}

	return serverID, nil
}

func (a *ClientPeerIDAuth) sign(challengeClientB64 string, hostname string) ([]byte, error) {
	return sign(a.PrivKey, PeerIDAuthScheme, []string{
		"challenge-client=" + challengeClientB64,
		fmt.Sprintf(`hostname="%s"`, hostname),
	})
}

// authSelfToServer performs the initial authentication request to the server. It authenticates the client to the server.
// Returns the Authorization value with libp2p-PeerID scheme to use for subsequent requests.
func (a *ClientPeerIDAuth) authSelfToServer(ctx context.Context, client *http.Client, myPeerID peer.ID, challengeServer []byte, authEndpoint string, hostname string) (string, error) {
	r, err := http.NewRequestWithContext(ctx, "GET", authEndpoint, nil)
	r.Host = hostname
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// do the initial auth request
	resp, err := client.Do(r)
	if err != nil {
		return "", fmt.Errorf("failed to do initial auth request: %w", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return "", nil
	}
	resp.Body.Close()

	authHeader := resp.Header.Get("WWW-Authenticate")
	f, err := parseAuthFields(authHeader, hostname, false)
	if err != nil {
		return "", fmt.Errorf("failed to parse our auth header: %w", err)
	}

	if len(f.challengeClientB64) == 0 {
		return "", errors.New("missing challenge")
	}

	sig, err := a.sign(f.challengeClientB64, hostname)
	if err != nil {
		return "", fmt.Errorf("failed to sign challenge: %w", err)
	}

	authValue := fmt.Sprintf(
		"%s peer-id=%s, sig=%s, opaque=%s, challenge-server=%s",
		PeerIDAuthScheme,
		myPeerID.String(),
		base64.URLEncoding.EncodeToString(sig),
		f.opaque,
		base64.URLEncoding.EncodeToString([]byte(challengeServer)),
	)

	// Attempt to read public key from our peer id
	_, err = myPeerID.ExtractPublicKey()
	if err == peer.ErrNoPublicKey {
		// If it fails we need to include the public key explicitly
		pubKey := a.PrivKey.GetPublic()
		pubKeyBytes, err := crypto.MarshalPublicKey(pubKey)
		if err != nil {
			return "", fmt.Errorf("failed to marshal public key: %w", err)
		}
		authValue += ", public-key=" + base64.URLEncoding.EncodeToString(pubKeyBytes)
	} else if err != nil {
		return "", fmt.Errorf("failed to extract public key: %w", err)
	}
	return authValue, nil
}

func (a *ClientPeerIDAuth) verifySigFromServer(r authFields, myPeerID peer.ID, challengeServer []byte) (peer.ID, error) {
	partsToVerify := make([]string, 0, 3)
	partsToVerify = append(partsToVerify, fmt.Sprintf(`hostname="%s"`, r.hostname))
	partsToVerify = append(partsToVerify, "challenge-server="+base64.URLEncoding.EncodeToString(challengeServer))
	partsToVerify = append(partsToVerify, "client="+myPeerID.String())

	err := verifySig(r.pubKey, PeerIDAuthScheme, partsToVerify, r.signature)
	if err != nil {
		return "", fmt.Errorf("failed to verify signature: %s", err)
	}
	return peer.IDFromPublicKey(r.pubKey)
}
