package libp2phttp

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"net/http"
	"strings"

	"github.com/flynn/noise"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/security/noise/pb"
	"github.com/multiformats/go-multibase"
	"google.golang.org/protobuf/proto"
)

const payloadSigPrefix = "noise-libp2p-static-key:"

type minioSHAFn struct{}

func (h minioSHAFn) Hash() hash.Hash  { return sha256.New() }
func (h minioSHAFn) HashName() string { return "SHA256" }

var shaHashFn noise.HashFunc = minioSHAFn{}
var cipherSuite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, shaHashFn)

type AuthState struct {
	hs *noise.HandshakeState
}

func WithNoiseAuthentication(hostKey crypto.PrivKey, requestHeader http.Header) (AuthState, error) {
	s := AuthState{}
	kp, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		return s, fmt.Errorf("error generating static keypair: %w", err)
	}

	cfg := noise.Config{
		CipherSuite:   cipherSuite,
		Pattern:       noise.HandshakeIX,
		Initiator:     true,
		StaticKeypair: kp,
		Prologue:      nil,
	}

	s.hs, err = noise.NewHandshakeState(cfg)
	if err != nil {
		return s, fmt.Errorf("error initializing handshake state: %w", err)
	}

	payload, err := generateNoisePayload(hostKey, kp, nil)
	if err != nil {
		return s, fmt.Errorf("error generating noise payload: %w", err)
	}

	// Allocate a buffer on the stack for the handshake message
	hbuf := [2 << 10]byte{}
	authMsg, _, _, err := s.hs.WriteMessage(hbuf[:0], payload)
	if err != nil {
		return s, fmt.Errorf("error writing handshake message: %w", err)
	}
	authMsgEncoded, err := multibase.Encode(multibase.Encodings["base32"], authMsg)
	if err != nil {
		return s, fmt.Errorf("error encoding handshake message: %w", err)
	}

	requestHeader.Set("Authorization", "Libp2p-Noise-IX "+authMsgEncoded)

	return s, nil
}

// AuthenticateClient verifies the Authorization header of the request and sets
// the Authentication-Info response header to allow the client to authenticate
// the server. Returns the peer.ID of the authenticated client.
// Returns an empty peer.ID if the client did not authenticate itself either by sending
// sending a `Libp2p-Noise-NX` Authorization header, or by not sending a `Libp2p-Noise-IX` Authorization header.
func AuthenticateClient(hostKey crypto.PrivKey, responseHeader http.Header, request *http.Request) (peer.ID, error) {
	authValue := request.Header.Get("Authorization")
	authMethod := strings.SplitN(authValue, " ", 2)
	if len(authMethod) != 2 || authMethod[0] != "Libp2p-Noise-IX" {
		return "", nil
	}

	// Decode the handshake message
	_, authMsg, err := multibase.Decode(authMethod[1])
	if err != nil {
		return "", fmt.Errorf("error decoding handshake message: %w", err)
	}

	kp, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("error generating static keypair: %w", err)
	}

	cfg := noise.Config{
		CipherSuite:   cipherSuite,
		Pattern:       noise.HandshakeIX,
		Initiator:     false,
		StaticKeypair: kp,
		Prologue:      nil,
	}

	hs, err := noise.NewHandshakeState(cfg)
	if err != nil {
		return "", fmt.Errorf("error initializing handshake state: %w", err)
	}

	// Allocate a buffer on the stack for the payload
	hbuf := [2 << 10]byte{}

	payload, _, _, err := hs.ReadMessage(hbuf[:0], authMsg)
	if err != nil {
		return "", fmt.Errorf("error reading handshake message: %w", err)
	}

	// TODO handle the peer not sending a Static key (handle Libp2p-Noise-NX)
	remotePeer, _, err := handleRemoteHandshakePayload(payload, hs.PeerStatic())
	if err != nil {
		return "", fmt.Errorf("error handling remote handshake payload: %w", err)
	}

	sni := ""
	if request.TLS != nil {
		sni = request.TLS.ServerName
	}

	payload, err = generateNoisePayload(hostKey, kp, &pb.NoiseExtensions{
		SNI: &sni,
	})
	if err != nil {
		return "", fmt.Errorf("error generating noise payload: %w", err)
	}

	authInfoMsg, _, _, err := hs.WriteMessage(hbuf[:0], payload)
	if err != nil {
		return "", fmt.Errorf("error writing handshake message: %w", err)
	}

	authInfoMsgEncoded, err := multibase.Encode(multibase.Encodings["base32"], authInfoMsg)
	if err != nil {
		return "", fmt.Errorf("error encoding handshake message: %w", err)
	}
	responseHeader.Set("Authentication-Info", authMethod[0]+" "+authInfoMsgEncoded)

	return remotePeer, nil
}

// AuthenticateServer returns the peer.ID of the server. It returns an error if the response does not include authentication info
func (s AuthState) AuthenticateServer(expectedSNI string, responseHeader http.Header) (peer.ID, error) {
	authValue := responseHeader.Get("Authentication-Info")
	authMethod := strings.SplitN(authValue, " ", 2)
	if len(authMethod) != 2 || authMethod[0] != "Libp2p-Noise-IX" {
		return "", errors.New("response does not include noise authentication info")
	}

	// Decode the handshake message
	_, authMsg, err := multibase.Decode(authMethod[1])
	if err != nil {
		return "", fmt.Errorf("error decoding handshake message: %w", err)
	}

	// Allocate a buffer on the stack for the payload
	hbuf := [2 << 10]byte{}

	payload, cs1, cs2, err := s.hs.ReadMessage(hbuf[:0], authMsg)
	if err != nil {
		return "", fmt.Errorf("error reading handshake message: %w", err)
	}

	if cs1 == nil || cs2 == nil {
		return "", errors.New("expected ciphersuites to be present")
	}

	server, extensions, err := handleRemoteHandshakePayload(payload, s.hs.PeerStatic())
	if err != nil {
		return "", fmt.Errorf("error handling remote handshake payload: %w", err)
	}

	if expectedSNI != "" {
		if extensions == nil {
			return "", errors.New("server is missing noise extensions")
		}

		if extensions.SNI == nil {
			return "", errors.New("server is missing SNI in noise extensions")
		}

		if *extensions.SNI != expectedSNI {
			return "", errors.New("server SNI in noise extension does not match expected SNI")
		}
	}

	return server, nil

}

func generateNoisePayload(hostKey crypto.PrivKey, localStatic noise.DHKey, ext *pb.NoiseExtensions) ([]byte, error) {
	// obtain the public key from the handshake session, so we can sign it with
	// our libp2p secret key.
	localKeyRaw, err := crypto.MarshalPublicKey(hostKey.GetPublic())
	if err != nil {
		return nil, fmt.Errorf("error serializing libp2p identity key: %w", err)
	}

	// prepare payload to sign; perform signature.
	toSign := append([]byte(payloadSigPrefix), localStatic.Public...)
	signedPayload, err := hostKey.Sign(toSign)
	if err != nil {
		return nil, fmt.Errorf("error sigining handshake payload: %w", err)
	}

	// create payload
	payloadEnc, err := proto.Marshal(&pb.NoiseHandshakePayload{
		IdentityKey: localKeyRaw,
		IdentitySig: signedPayload,
		Extensions:  ext,
	})
	if err != nil {
		return nil, fmt.Errorf("error marshaling handshake payload: %w", err)
	}
	return payloadEnc, nil
}

// handleRemoteHandshakePayload unmarshals the handshake payload object sent
// by the remote peer and validates the signature against the peer's static Noise key.
// It returns the data attached to the payload.
func handleRemoteHandshakePayload(payload []byte, remoteStatic []byte) (peer.ID, *pb.NoiseExtensions, error) {
	// unmarshal payload
	nhp := new(pb.NoiseHandshakePayload)
	err := proto.Unmarshal(payload, nhp)
	if err != nil {
		return "", nil, fmt.Errorf("error unmarshaling remote handshake payload: %w", err)
	}

	// unpack remote peer's public libp2p key
	remotePubKey, err := crypto.UnmarshalPublicKey(nhp.GetIdentityKey())
	if err != nil {
		return "", nil, err
	}
	id, err := peer.IDFromPublicKey(remotePubKey)
	if err != nil {
		return "", nil, err
	}

	// verify payload is signed by asserted remote libp2p key.
	sig := nhp.GetIdentitySig()
	msg := append([]byte(payloadSigPrefix), remoteStatic...)
	ok, err := remotePubKey.Verify(msg, sig)
	if err != nil {
		return "", nil, fmt.Errorf("error verifying signature: %w", err)
	} else if !ok {
		return "", nil, fmt.Errorf("handshake signature invalid")
	}

	return id, nhp.Extensions, nil
}
