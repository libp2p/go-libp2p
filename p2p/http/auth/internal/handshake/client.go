package handshake

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

type peerIDAuthClientState int

const (
	peerIDAuthClientStateSignChallenge peerIDAuthClientState = iota
	peerIDAuthClientStateVerifyChallenge
	peerIDAuthClientStateDone // We have the bearer token, and there's nothing left to do

	// Client initiated handshake
	peerIDAuthClientInitiateChallenge
	peerIDAuthClientStateVerifyAndSignChallenge
	peerIDAuthClientStateWaitingForBearer
)

type PeerIDAuthHandshakeClient struct {
	Hostname string
	PrivKey  crypto.PrivKey

	serverPeerID    peer.ID
	serverPubKey    crypto.PubKey
	state           peerIDAuthClientState
	p               params
	hb              headerBuilder
	challengeServer []byte
	buf             [128]byte
}

var errMissingChallenge = errors.New("missing challenge")

func (h *PeerIDAuthHandshakeClient) SetInitiateChallenge() {
	h.state = peerIDAuthClientInitiateChallenge
}

func (h *PeerIDAuthHandshakeClient) ParseHeader(header http.Header) error {
	if h.state == peerIDAuthClientStateDone || h.state == peerIDAuthClientInitiateChallenge {
		return nil
	}
	h.p = params{}

	var headerVal []byte
	switch h.state {
	case peerIDAuthClientStateSignChallenge, peerIDAuthClientStateVerifyAndSignChallenge:
		headerVal = []byte(header.Get("WWW-Authenticate"))
	case peerIDAuthClientStateVerifyChallenge, peerIDAuthClientStateWaitingForBearer:
		headerVal = []byte(header.Get("Authentication-Info"))
	}

	if len(headerVal) == 0 {
		return errMissingChallenge
	}

	err := h.p.parsePeerIDAuthSchemeParams(headerVal)
	if err != nil {
		return err
	}

	if h.serverPubKey == nil && len(h.p.publicKeyB64) > 0 {
		serverPubKeyBytes, err := base64.URLEncoding.AppendDecode(nil, h.p.publicKeyB64)
		if err != nil {
			return err
		}
		h.serverPubKey, err = crypto.UnmarshalPublicKey(serverPubKeyBytes)
		if err != nil {
			return err
		}
		h.serverPeerID, err = peer.IDFromPublicKey(h.serverPubKey)
		if err != nil {
			return err
		}
	}

	return err
}

func (h *PeerIDAuthHandshakeClient) Run() error {
	if h.state == peerIDAuthClientStateDone {
		return nil
	}

	h.hb.clear()
	clientPubKeyBytes, err := crypto.MarshalPublicKey(h.PrivKey.GetPublic())
	if err != nil {
		return err
	}
	switch h.state {
	case peerIDAuthClientInitiateChallenge:
		h.hb.writeScheme(PeerIDAuthScheme)
		h.addChallengeServerParam()
		h.hb.writeParamB64(nil, "public-key", clientPubKeyBytes)
		h.state = peerIDAuthClientStateVerifyAndSignChallenge
		return nil
	case peerIDAuthClientStateVerifyAndSignChallenge:
		if len(h.p.sigB64) == 0 && len(h.p.challengeClient) != 0 {
			// The server refused a client initiated handshake, so we need run the server initiated handshake
			h.state = peerIDAuthClientStateSignChallenge
			return h.Run()
		}
		if err := h.verifySig(clientPubKeyBytes); err != nil {
			return err
		}

		h.hb.writeScheme(PeerIDAuthScheme)
		h.hb.writeParam("opaque", h.p.opaqueB64)
		h.addSigParam()
		h.state = peerIDAuthClientStateWaitingForBearer
		return nil

	case peerIDAuthClientStateWaitingForBearer:
		h.hb.writeScheme(PeerIDAuthScheme)
		h.hb.writeParam("bearer", h.p.bearerTokenB64)
		h.state = peerIDAuthClientStateDone
		return nil

	case peerIDAuthClientStateSignChallenge:
		if len(h.p.challengeClient) < challengeLen {
			return errors.New("challenge too short")
		}

		h.hb.writeScheme(PeerIDAuthScheme)
		h.hb.writeParamB64(nil, "public-key", clientPubKeyBytes)
		if err := h.addChallengeServerParam(); err != nil {
			return err
		}
		if err := h.addSigParam(); err != nil {
			return err
		}
		h.hb.writeParam("opaque", h.p.opaqueB64)

		h.state = peerIDAuthClientStateVerifyChallenge
		return nil
	case peerIDAuthClientStateVerifyChallenge:
		if err := h.verifySig(clientPubKeyBytes); err != nil {
			return err
		}

		h.hb.writeScheme(PeerIDAuthScheme)
		h.hb.writeParam("bearer", h.p.bearerTokenB64)
		h.state = peerIDAuthClientStateDone

		return nil
	}

	return errors.New("unhandled state")
}

func (h *PeerIDAuthHandshakeClient) addChallengeServerParam() error {
	_, err := io.ReadFull(randReader, h.buf[:challengeLen])
	if err != nil {
		return err
	}
	h.challengeServer = base64.URLEncoding.AppendEncode(nil, h.buf[:challengeLen])
	clear(h.buf[:challengeLen])
	h.hb.writeParam("challenge-server", h.challengeServer)
	return nil
}

func (h *PeerIDAuthHandshakeClient) verifySig(clientPubKeyBytes []byte) error {
	if len(h.p.sigB64) == 0 {
		return errors.New("signature not set")
	}
	sig, err := base64.URLEncoding.AppendDecode(nil, h.p.sigB64)
	if err != nil {
		return fmt.Errorf("failed to decode signature: %w", err)
	}
	err = verifySig(h.serverPubKey, PeerIDAuthScheme, []sigParam{
		{"challenge-server", h.challengeServer},
		{"client-public-key", clientPubKeyBytes},
		{"hostname", []byte(h.Hostname)},
	}, sig)
	return err
}

func (h *PeerIDAuthHandshakeClient) addSigParam() error {
	if h.serverPubKey == nil {
		return errors.New("server public key not set")
	}
	serverPubKeyBytes, err := crypto.MarshalPublicKey(h.serverPubKey)
	if err != nil {
		return err
	}
	clientSig, err := sign(h.PrivKey, PeerIDAuthScheme, []sigParam{
		{"challenge-client", h.p.challengeClient},
		{"server-public-key", serverPubKeyBytes},
		{"hostname", []byte(h.Hostname)},
	})
	if err != nil {
		return fmt.Errorf("failed to sign challenge: %w", err)
	}
	h.hb.writeParamB64(nil, "sig", clientSig)
	return nil

}

// PeerID returns the peer ID of the authenticated client.
func (h *PeerIDAuthHandshakeClient) PeerID() (peer.ID, error) {
	switch h.state {
	case peerIDAuthClientStateDone:
	case peerIDAuthClientStateWaitingForBearer:
	default:
		return "", errors.New("server not authenticated yet")
	}

	if h.serverPeerID == "" {
		return "", errors.New("peer ID not set")
	}
	return h.serverPeerID, nil
}

func (h *PeerIDAuthHandshakeClient) AddHeader(hdr http.Header) {
	hdr.Set("Authorization", h.hb.b.String())
}

// BearerToken returns the server given bearer token for the client. Set this on
// the Authorization header in the client's request.
func (h *PeerIDAuthHandshakeClient) BearerToken() string {
	if h.state != peerIDAuthClientStateDone {
		return ""
	}
	return h.hb.b.String()
}

func (h *PeerIDAuthHandshakeClient) ServerAuthenticated() bool {
	switch h.state {
	case peerIDAuthClientStateDone:
	case peerIDAuthClientStateWaitingForBearer:
	default:
		return false
	}

	return h.serverPeerID != ""
}

func (h *PeerIDAuthHandshakeClient) HandshakeDone() bool {
	return h.state == peerIDAuthClientStateDone
}
