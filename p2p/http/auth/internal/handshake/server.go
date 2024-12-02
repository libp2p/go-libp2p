package handshake

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

var (
	ErrExpiredChallenge = errors.New("challenge expired")
	ErrExpiredToken     = errors.New("token expired")
	ErrInvalidHMAC      = errors.New("invalid HMAC")
)

const challengeTTL = 5 * time.Minute

type peerIDAuthServerState int

const (
	// Server initiated
	peerIDAuthServerStateChallengeClient peerIDAuthServerState = iota
	peerIDAuthServerStateVerifyChallenge
	peerIDAuthServerStateVerifyBearer

	// Client initiated
	peerIDAuthServerStateSignChallenge
)

type opaqueState struct {
	IsToken         bool      `json:"is-token,omitempty"`
	ClientPublicKey []byte    `json:"client-public-key,omitempty"`
	PeerID          peer.ID   `json:"peer-id,omitempty"`
	ChallengeClient string    `json:"challenge-client,omitempty"`
	Hostname        string    `json:"hostname"`
	CreatedTime     time.Time `json:"created-time"`
}

// Marshal serializes the state by appending it to the byte slice.
func (o *opaqueState) Marshal(hmac hash.Hash, b []byte) ([]byte, error) {
	hmac.Reset()
	fieldsMarshalled, err := json.Marshal(o)
	if err != nil {
		return b, err
	}
	_, err = hmac.Write(fieldsMarshalled)
	if err != nil {
		return b, err
	}
	b = hmac.Sum(b)
	b = append(b, fieldsMarshalled...)
	return b, nil
}

func (o *opaqueState) Unmarshal(hmacImpl hash.Hash, d []byte) error {
	hmacImpl.Reset()
	if len(d) < hmacImpl.Size() {
		return ErrInvalidHMAC
	}
	hmacVal := d[:hmacImpl.Size()]
	fields := d[hmacImpl.Size():]
	_, err := hmacImpl.Write(fields)
	if err != nil {
		return err
	}
	expectedHmac := hmacImpl.Sum(nil)
	if !hmac.Equal(hmacVal, expectedHmac) {
		return ErrInvalidHMAC
	}

	err = json.Unmarshal(fields, &o)
	if err != nil {
		return err
	}
	return nil
}

type PeerIDAuthHandshakeServer struct {
	Hostname string
	PrivKey  crypto.PrivKey
	TokenTTL time.Duration
	// used to authenticate opaque blobs and tokens
	Hmac hash.Hash

	ran bool
	buf [1024]byte

	state peerIDAuthServerState
	p     params
	hb    headerBuilder

	opaque opaqueState
}

var errInvalidHeader = errors.New("invalid header")

func (h *PeerIDAuthHandshakeServer) Reset() {
	h.Hmac.Reset()
	h.ran = false
	clear(h.buf[:])
	h.state = 0
	h.p = params{}
	h.hb.clear()
	h.opaque = opaqueState{}
}

func (h *PeerIDAuthHandshakeServer) ParseHeaderVal(headerVal []byte) error {
	if len(headerVal) == 0 {
		// We are in the initial state. Nothing to parse.
		return nil
	}
	err := h.p.parsePeerIDAuthSchemeParams(headerVal)
	if err != nil {
		return err
	}
	switch {
	case h.p.sigB64 != nil && h.p.opaqueB64 != nil:
		h.state = peerIDAuthServerStateVerifyChallenge
	case h.p.bearerTokenB64 != nil:
		h.state = peerIDAuthServerStateVerifyBearer
	case h.p.challengeServer != nil && h.p.publicKeyB64 != nil:
		h.state = peerIDAuthServerStateSignChallenge
	default:
		return errInvalidHeader

	}
	return nil
}

func (h *PeerIDAuthHandshakeServer) Run() error {
	h.ran = true
	switch h.state {
	case peerIDAuthServerStateSignChallenge:
		h.hb.writeScheme(PeerIDAuthScheme)
		if err := h.addChallengeClientParam(); err != nil {
			return err
		}
		if err := h.addPublicKeyParam(); err != nil {
			return err
		}

		publicKeyBytes, err := base64.URLEncoding.AppendDecode(nil, h.p.publicKeyB64)
		if err != nil {
			return err
		}
		h.opaque.ClientPublicKey = publicKeyBytes
		if err := h.addServerSigParam(publicKeyBytes); err != nil {
			return err
		}
		if err := h.addOpaqueParam(); err != nil {
			return err
		}
	case peerIDAuthServerStateChallengeClient:
		h.hb.writeScheme(PeerIDAuthScheme)
		if err := h.addChallengeClientParam(); err != nil {
			return err
		}
		if err := h.addPublicKeyParam(); err != nil {
			return err
		}
		if err := h.addOpaqueParam(); err != nil {
			return err
		}
	case peerIDAuthServerStateVerifyChallenge:
		opaque, err := base64.URLEncoding.AppendDecode(h.buf[:0], h.p.opaqueB64)
		if err != nil {
			return err
		}
		err = h.opaque.Unmarshal(h.Hmac, opaque)
		if err != nil {
			return err
		}

		if nowFn().After(h.opaque.CreatedTime.Add(challengeTTL)) {
			return ErrExpiredChallenge
		}
		if h.opaque.IsToken {
			return errors.New("expected challenge, got token")
		}

		if h.Hostname != h.opaque.Hostname {
			return errors.New("hostname in opaque mismatch")
		}

		var publicKeyBytes []byte
		clientInitiatedHandshake := h.opaque.ClientPublicKey != nil

		if clientInitiatedHandshake {
			publicKeyBytes = h.opaque.ClientPublicKey
		} else {
			if len(h.p.publicKeyB64) == 0 {
				return errors.New("missing public key")
			}
			var err error
			publicKeyBytes, err = base64.URLEncoding.AppendDecode(nil, h.p.publicKeyB64)
			if err != nil {
				return err
			}
		}
		pubKey, err := crypto.UnmarshalPublicKey(publicKeyBytes)
		if err != nil {
			return err
		}
		if err := h.verifySig(pubKey); err != nil {
			return err
		}

		peerID, err := peer.IDFromPublicKey(pubKey)
		if err != nil {
			return err
		}

		// And create a bearer token for the client
		h.opaque = opaqueState{
			IsToken:     true,
			PeerID:      peerID,
			Hostname:    h.Hostname,
			CreatedTime: nowFn(),
		}

		h.hb.writeScheme(PeerIDAuthScheme)

		if !clientInitiatedHandshake {
			if err := h.addServerSigParam(publicKeyBytes); err != nil {
				return err
			}
		}
		if err := h.addBearerParam(); err != nil {
			return err
		}
	case peerIDAuthServerStateVerifyBearer:
		bearerToken, err := base64.URLEncoding.AppendDecode(h.buf[:0], h.p.bearerTokenB64)
		if err != nil {
			return err
		}
		err = h.opaque.Unmarshal(h.Hmac, bearerToken)
		if err != nil {
			return err
		}

		if !h.opaque.IsToken {
			return errors.New("expected token, got challenge")
		}

		if nowFn().After(h.opaque.CreatedTime.Add(h.TokenTTL)) {
			return ErrExpiredToken
		}

		return nil
	default:
		return errors.New("unhandled state")
	}

	return nil
}

func (h *PeerIDAuthHandshakeServer) addChallengeClientParam() error {
	_, err := io.ReadFull(randReader, h.buf[:challengeLen])
	if err != nil {
		return err
	}
	encodedChallenge := base64.URLEncoding.AppendEncode(h.buf[challengeLen:challengeLen], h.buf[:challengeLen])
	h.opaque.ChallengeClient = string(encodedChallenge)
	h.opaque.Hostname = h.Hostname
	h.opaque.CreatedTime = nowFn()
	h.hb.writeParam("challenge-client", encodedChallenge)
	return nil
}

func (h *PeerIDAuthHandshakeServer) addOpaqueParam() error {
	opaqueVal, err := h.opaque.Marshal(h.Hmac, h.buf[:0])
	if err != nil {
		return err
	}
	h.hb.writeParamB64(h.buf[len(opaqueVal):], "opaque", opaqueVal)
	return nil
}

func (h *PeerIDAuthHandshakeServer) addServerSigParam(clientPublicKeyBytes []byte) error {
	if len(h.p.challengeServer) < challengeLen {
		return errors.New("challenge too short")
	}
	serverSig, err := sign(h.PrivKey, PeerIDAuthScheme, []sigParam{
		{"challenge-server", h.p.challengeServer},
		{"client-public-key", clientPublicKeyBytes},
		{"hostname", []byte(h.Hostname)},
	})
	if err != nil {
		return fmt.Errorf("failed to sign challenge: %w", err)
	}
	h.hb.writeParamB64(h.buf[:], "sig", serverSig)
	return nil
}

func (h *PeerIDAuthHandshakeServer) addBearerParam() error {
	bearerToken, err := h.opaque.Marshal(h.Hmac, h.buf[:0])
	if err != nil {
		return err
	}
	h.hb.writeParamB64(h.buf[len(bearerToken):], "bearer", bearerToken)
	return nil
}

func (h *PeerIDAuthHandshakeServer) addPublicKeyParam() error {
	serverPubKey := h.PrivKey.GetPublic()
	pubKeyBytes, err := crypto.MarshalPublicKey(serverPubKey)
	if err != nil {
		return err
	}
	h.hb.writeParamB64(h.buf[:], "public-key", pubKeyBytes)
	return nil
}

func (h *PeerIDAuthHandshakeServer) verifySig(clientPubKey crypto.PubKey) error {
	serverPubKey := h.PrivKey.GetPublic()
	serverPubKeyBytes, err := crypto.MarshalPublicKey(serverPubKey)
	if err != nil {
		return err
	}
	sig, err := base64.URLEncoding.AppendDecode(h.buf[:0], h.p.sigB64)
	if err != nil {
		return fmt.Errorf("failed to decode signature: %w", err)
	}
	err = verifySig(clientPubKey, PeerIDAuthScheme, []sigParam{
		{k: "challenge-client", v: []byte(h.opaque.ChallengeClient)},
		{k: "server-public-key", v: serverPubKeyBytes},
		{k: "hostname", v: []byte(h.Hostname)},
	}, sig)
	if err != nil {
		return err
	}
	return nil
}

// PeerID returns the peer ID of the authenticated client.
func (h *PeerIDAuthHandshakeServer) PeerID() (peer.ID, error) {
	if !h.ran {
		return "", errNotRan
	}
	switch h.state {
	case peerIDAuthServerStateVerifyChallenge:
	case peerIDAuthServerStateVerifyBearer:
	default:
		return "", errors.New("not in proper state")
	}
	if h.opaque.PeerID == "" {
		return "", errors.New("peer ID not set")
	}
	return h.opaque.PeerID, nil
}

func (h *PeerIDAuthHandshakeServer) SetHeader(hdr http.Header) {
	if !h.ran {
		return
	}
	defer h.hb.clear()
	switch h.state {
	case peerIDAuthServerStateChallengeClient, peerIDAuthServerStateSignChallenge:
		hdr.Set("WWW-Authenticate", h.hb.b.String())
	case peerIDAuthServerStateVerifyChallenge:
		hdr.Set("Authentication-Info", h.hb.b.String())
	case peerIDAuthServerStateVerifyBearer:
		// For completeness. Nothing to do
	}
}
