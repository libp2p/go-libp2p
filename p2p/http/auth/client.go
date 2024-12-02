package httppeeridauth

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/http/auth/internal/handshake"
)

type ClientPeerIDAuth struct {
	PrivKey  crypto.PrivKey
	TokenTTL time.Duration

	tm tokenMap
}

// AuthenticatedDo is like http.Client.Do, but it does the libp2p peer ID auth
// handshake if needed.
//
// It is recommended to pass in an http.Request with `GetBody` set, so that this
// method can retry sending the request in case a previously used token has
// expired.
func (a *ClientPeerIDAuth) AuthenticatedDo(client *http.Client, req *http.Request) (peer.ID, *http.Response, error) {
	hostname := req.Host
	ti, hasToken := a.tm.get(hostname, a.TokenTTL)
	handshake := handshake.PeerIDAuthHandshakeClient{
		Hostname: hostname,
		PrivKey:  a.PrivKey,
	}

	if hasToken {
		// We have a token. Attempt to use that, but fallback to server initiated challenge if it fails.
		peer, resp, err := a.doWithToken(client, req, ti)
		switch {
		case err == nil:
			return peer, resp, nil
		case errors.Is(err, errTokenRejected):
			// Token was rejected, we need to re-authenticate
			break
		default:
			return "", nil, err
		}

		// Token didn't work, we need to re-authenticate.
		// Run the server-initiated handshake
		req = req.Clone(req.Context())
		req.Body, err = req.GetBody()
		if err != nil {
			return "", nil, err
		}

		handshake.ParseHeader(resp.Header)
	} else {
		// We didn't have a handshake token, so we initiate the handshake.
		// If our token was rejected, the server initiates the handshake.
		handshake.SetInitiateChallenge()
	}

	serverPeerID, resp, err := a.runHandshake(client, req, clearBody(req), &handshake)
	if err != nil {
		return "", nil, fmt.Errorf("failed to run handshake: %w", err)
	}
	a.tm.set(hostname, tokenInfo{
		token:      handshake.BearerToken(),
		insertedAt: time.Now(),
		peerID:     serverPeerID,
	})
	return serverPeerID, resp, nil
}

func (a *ClientPeerIDAuth) runHandshake(client *http.Client, req *http.Request, b bodyMeta, hs *handshake.PeerIDAuthHandshakeClient) (peer.ID, *http.Response, error) {
	maxSteps := 5 // Avoid infinite loops in case of buggy handshake. Shouldn't happen.
	var resp *http.Response

	err := hs.Run()
	if err != nil {
		return "", nil, err
	}

	sentBody := false
	for !hs.HandshakeDone() || !sentBody {
		req = req.Clone(req.Context())
		hs.AddHeader(req.Header)
		if hs.ServerAuthenticated() {
			sentBody = true
			b.setBody(req)
		}

		resp, err = client.Do(req)
		if err != nil {
			return "", nil, err
		}

		hs.ParseHeader(resp.Header)
		err = hs.Run()
		if err != nil {
			resp.Body.Close()
			return "", nil, err
		}

		if maxSteps--; maxSteps == 0 {
			return "", nil, errors.New("handshake took too many steps")
		}
	}

	p, err := hs.PeerID()
	if err != nil {
		resp.Body.Close()
		return "", nil, err
	}
	return p, resp, nil
}

var errTokenRejected = errors.New("token rejected")

func (a *ClientPeerIDAuth) doWithToken(client *http.Client, req *http.Request, ti tokenInfo) (peer.ID, *http.Response, error) {
	// Try to make the request with the token
	req.Header.Set("Authorization", ti.token)
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		// our token is still valid
		return ti.peerID, resp, nil
	}
	if req.GetBody == nil {
		// We can't retry this request even if we wanted to.
		// Return the response and an error
		return "", resp, errors.New("expired token. Couldn't run handshake because req.GetBody is nil")
	}
	resp.Body.Close()

	return "", resp, errTokenRejected
}

type bodyMeta struct {
	body          io.ReadCloser
	contentLength int64
	getBody       func() (io.ReadCloser, error)
}

func clearBody(req *http.Request) bodyMeta {
	defer func() {
		req.Body = nil
		req.ContentLength = 0
		req.GetBody = nil
	}()
	return bodyMeta{body: req.Body, contentLength: req.ContentLength, getBody: req.GetBody}
}

func (b *bodyMeta) setBody(req *http.Request) {
	req.Body = b.body
	req.ContentLength = b.contentLength
	req.GetBody = b.getBody
}

type tokenInfo struct {
	token      string
	insertedAt time.Time
	peerID     peer.ID
}

type tokenMap struct {
	tokenMapMu sync.Mutex
	tokenMap   map[string]tokenInfo
}

func (tm *tokenMap) get(hostname string, ttl time.Duration) (tokenInfo, bool) {
	tm.tokenMapMu.Lock()
	defer tm.tokenMapMu.Unlock()

	ti, ok := tm.tokenMap[hostname]
	if ok && ttl != 0 && time.Since(ti.insertedAt) > ttl {
		delete(tm.tokenMap, hostname)
		return tokenInfo{}, false
	}
	return ti, ok
}

func (tm *tokenMap) set(hostname string, ti tokenInfo) {
	tm.tokenMapMu.Lock()
	defer tm.tokenMapMu.Unlock()
	if tm.tokenMap == nil {
		tm.tokenMap = make(map[string]tokenInfo)
	}
	tm.tokenMap[hostname] = ti
}
