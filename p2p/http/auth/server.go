package httppeeridauth

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	pool "github.com/libp2p/go-buffer-pool"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

const maxAuthHeaderSize = 8192

const challengeTTL = 5 * time.Minute

type ServerPeerIDAuth struct {
	PrivKey        crypto.PrivKey
	ValidHostnames map[string]struct{}
	TokenTTL       time.Duration
	Next           http.Handler
	// InsecureNoTLS is a flag that allows the server to accept requests without a TLS ServerName. Used only for testing.
	InsecureNoTLS bool
}

var errMissingAuthHeader = errors.New("missing header")

// ServeHTTP implements the http.Handler interface for PeerIDAuth. It will
// attempt to authenticate the request using using the libp2p peer ID auth
// scheme. If a Next handler is set, it will be called on authenticated
// requests.
func (a *ServerPeerIDAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hostname := r.Host
	if !a.InsecureNoTLS {
		if r.TLS == nil {
			log.Debugf("No TLS connection, and InsecureNoTLS is false")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if hostname != r.TLS.ServerName {
			log.Debugf("Unauthorized request for host %s: hostname mismatch. Expected %s", hostname, r.TLS.ServerName)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	// Do they have a bearer token?
	_, err := a.UnwrapBearerToken(r, hostname)
	if err != nil {
		// No bearer token, let's try peer ID auth
		_, ok := a.ValidHostnames[hostname]
		if !ok {
			log.Debugf("Unauthorized request from %s: invalid hostname", hostname)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		f, err := parseAuthFields(r.Header.Get("Authorization"), hostname, true)
		if err != nil {
			a.serveAuthReq(w)
			return
		}

		var id peer.ID
		id, err = a.authenticate(f)
		if err != nil {
			log.Debugf("failed to authenticate: %s", err)
			a.serveAuthReq(w)
			return
		}

		tok := bearerToken{
			peer:      id,
			hostname:  f.hostname,
			createdAt: time.Now(),
		}
		b := pool.Get(4096)
		defer pool.Put(b)
		b, err = genBearerTokenBlob(b[:0], a.PrivKey, tok)
		if err != nil {
			log.Debugf("failed to generate bearer token: %s", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		blobB64 := pool.Get(len(BearerAuthScheme) + 1 + base64.URLEncoding.EncodedLen(len(b)))
		defer pool.Put(blobB64)

		blobB64 = blobB64[:0]
		blobB64 = append(blobB64, BearerAuthScheme...)
		blobB64 = append(blobB64, ' ')
		blobB64 = b64AppendEncode(blobB64, b)

		w.Header().Set("Authorization", string(blobB64))

		if base64.URLEncoding.DecodedLen(len(f.challengeServerB64)) >= challengeLen {
			clientID, err := peer.IDFromPublicKey(f.pubKey)
			if err != nil {
				log.Debugf("failed to get peer ID: %s", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			buf, err := a.signChallengeServer(f.challengeServerB64, clientID, f.hostname)
			if err != nil {
				log.Debugf("failed to sign challenge: %s", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			myId, err := peer.IDFromPublicKey(a.PrivKey.GetPublic())
			if err != nil {
				log.Debugf("failed to get peer ID: %s", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			authInfoVal := fmt.Sprintf("%s peer-id=%s, sig=%s", PeerIDAuthScheme, myId.String(), base64.URLEncoding.EncodeToString(buf))
			w.Header().Set("Authentication-Info", authInfoVal)
		}

		if a.Next != nil {
			// Set the token on the request so the next handler can read it
			authHeader := r.Header.Get("Authorization")
			if authHeader != "" {
				authHeader = string(blobB64) + ", " + authHeader
			} else {
				authHeader = string(blobB64)
			}
			r.Header.Set("Authorization", string(blobB64)+", "+authHeader)
		}
	}

	if a.Next == nil {
		// No next handler, just return
		w.WriteHeader(http.StatusOK)
		return
	}
	a.Next.ServeHTTP(w, r)
}

func (a *ServerPeerIDAuth) signChallengeServer(challengeServerB64 string, client peer.ID, hostname string) ([]byte, error) {
	if len(challengeServerB64) == 0 {
		return nil, errors.New("missing challenge")
	}
	partsToSign := []string{
		"challenge-server=" + challengeServerB64,
		"client=" + client.String(),
		fmt.Sprintf(`hostname="%s"`, hostname),
	}
	sig, err := sign(a.PrivKey, PeerIDAuthScheme, partsToSign)
	if err != nil {
		return nil, fmt.Errorf("failed to sign challenge: %w", err)
	}
	return sig, nil
}

func (a *ServerPeerIDAuth) authenticate(f authFields) (peer.ID, error) {
	partsToVerify := make([]string, 0, 2)
	o, err := getChallengeFromOpaque(a.PrivKey, []byte(f.opaque))
	if err != nil {
		return "", fmt.Errorf("failed to get challenge from opaque: %s", err)
	}
	if time.Now().After(o.createdTime.Add(challengeTTL)) {
		return "", errors.New("challenge expired")
	}
	challengeClient := o.challenge
	if len(challengeClient) > 0 {
		partsToVerify = append(partsToVerify, "challenge-client="+base64.URLEncoding.EncodeToString(challengeClient))
	}
	partsToVerify = append(partsToVerify, fmt.Sprintf(`hostname="%s"`, f.hostname))

	err = verifySig(f.pubKey, PeerIDAuthScheme, partsToVerify, f.signature)
	if err != nil {
		return "", fmt.Errorf("failed to verify signature: %s", err)
	}
	return peer.IDFromPublicKey(f.pubKey)
}

func (a *ServerPeerIDAuth) UnwrapBearerToken(r *http.Request, expectedHostname string) (peer.ID, error) {
	if !strings.Contains(r.Header.Get("Authorization"), BearerAuthScheme) {
		return "", errors.New("missing bearer auth scheme")
	}
	schemes, err := parseAuthHeader(r.Header.Get("Authorization"))
	if err != nil {
		return "", fmt.Errorf("failed to parse auth header: %w", err)
	}
	bearerScheme, ok := schemes[BearerAuthScheme]
	if !ok {
		return "", fmt.Errorf("missing bearer auth scheme")
	}
	return a.unwrapBearerToken(expectedHostname, bearerScheme)
}

func (a *ServerPeerIDAuth) unwrapBearerToken(expectedHostname string, s authScheme) (peer.ID, error) {
	buf := pool.Get(4096)
	defer pool.Put(buf)
	buf, err := b64AppendDecode(buf[:0], []byte(s.bearerToken))
	if err != nil {
		return "", fmt.Errorf("failed to decode bearer token: %w", err)
	}
	parsed, err := parseBearerTokenBlob(a.PrivKey, buf)
	if err != nil {
		return "", fmt.Errorf("failed to parse bearer token: %w", err)
	}
	if time.Now().After(parsed.createdAt.Add(a.TokenTTL)) {
		return "", fmt.Errorf("bearer token expired")
	}
	if parsed.hostname != expectedHostname {
		return "", fmt.Errorf("bearer token hostname mismatch")
	}
	return parsed.peer, nil
}

type bearerToken struct {
	peer      peer.ID
	hostname  string
	createdAt time.Time
}

func genBearerTokenBlob(buf []byte, privKey crypto.PrivKey, t bearerToken) ([]byte, error) {
	peerBytes, err := t.peer.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal peer ID: %w", err)
	}
	createdAtBytes, err := t.createdAt.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal createdAt: %w", err)
	}

	// Auth scheme prefix
	buf = append(buf, BearerAuthScheme...)
	buf = append(buf, ' ') // Space between auth scheme and the token

	// Peer ID
	buf = binary.AppendUvarint(buf, uint64(len(peerBytes)))
	buf = append(buf, peerBytes...)

	// Hostname
	buf = binary.AppendUvarint(buf, uint64(len(t.hostname)))
	buf = append(buf, t.hostname...)

	// Created at
	buf = binary.AppendUvarint(buf, uint64(len(createdAtBytes)))
	buf = append(buf, createdAtBytes...)

	sig, err := privKey.Sign(buf)
	if err != nil {
		return nil, fmt.Errorf("failed to sign bearer token: %w", err)
	}
	buf = append(buf, sig...)

	return buf, nil
}

func parseBearerTokenBlob(privKey crypto.PrivKey, blob []byte) (bearerToken, error) {
	originalSlice := blob

	// Auth scheme prefix +1 for space
	if len(BearerAuthScheme)+1 > len(blob) {
		return bearerToken{}, fmt.Errorf("bearer token too short")
	}
	hasPrefix := bytes.Equal([]byte(BearerAuthScheme), blob[:len(BearerAuthScheme)])
	if !hasPrefix {
		return bearerToken{}, fmt.Errorf("missing bearer token prefix")
	}
	blob = blob[len(BearerAuthScheme):]
	if blob[0] != ' ' {
		return bearerToken{}, fmt.Errorf("missing space after auth scheme")
	}
	blob = blob[1:]

	// Peer ID
	peerIDLen, n := binary.Uvarint(blob)
	if n <= 0 {
		return bearerToken{}, fmt.Errorf("failed to read peer ID length")
	}

	blob = blob[n:]
	if int(peerIDLen) > len(blob) {
		return bearerToken{}, fmt.Errorf("peer ID length is wrong")
	}
	var peer peer.ID
	err := peer.UnmarshalBinary(blob[:peerIDLen])
	if err != nil {
		return bearerToken{}, fmt.Errorf("failed to unmarshal peer ID: %w", err)
	}
	blob = blob[peerIDLen:]

	// Hostname
	hostnameLen, n := binary.Uvarint(blob)
	if n <= 0 {
		return bearerToken{}, fmt.Errorf("failed to read hostname length")
	}
	blob = blob[n:]
	if int(hostnameLen) > len(blob) {
		return bearerToken{}, fmt.Errorf("hostname length is wrong")
	}
	hostname := string(blob[:hostnameLen])
	blob = blob[hostnameLen:]

	// Created At
	createdAtLen, n := binary.Uvarint(blob)
	if n <= 0 {
		return bearerToken{}, fmt.Errorf("failed to read created at length")
	}
	blob = blob[n:]
	if int(createdAtLen) > len(blob) {
		return bearerToken{}, fmt.Errorf("created at length is wrong")
	}
	var createdAt time.Time
	err = createdAt.UnmarshalBinary(blob[:createdAtLen])
	if err != nil {
		return bearerToken{}, fmt.Errorf("failed to unmarshal created at: %w", err)
	}
	sig := blob[createdAtLen:]
	if len(sig) == 0 {
		return bearerToken{}, fmt.Errorf("missing signature")
	}

	blobWithoutSig := originalSlice[:len(originalSlice)-len(sig)]
	ok, err := privKey.GetPublic().Verify(blobWithoutSig, sig)
	if err != nil {
		return bearerToken{}, fmt.Errorf("failed to verify signature: %w", err)
	}
	if !ok {
		return bearerToken{}, fmt.Errorf("signature verification failed")
	}
	return bearerToken{peer: peer, hostname: hostname, createdAt: createdAt}, nil
}

type opaqueUnwrapped struct {
	challenge   []byte
	createdTime time.Time
}

func getChallengeFromOpaque(privKey crypto.PrivKey, opaqueB64 []byte) (opaqueUnwrapped, error) {
	if len(opaqueB64) == 0 {
		return opaqueUnwrapped{}, fmt.Errorf("missing opaque blob")
	}

	opaqueBlob := pool.Get(2048)
	defer pool.Put(opaqueBlob)
	opaqueBlob, err := b64AppendDecode(opaqueBlob[:0], opaqueB64)
	if err != nil {
		return opaqueUnwrapped{}, fmt.Errorf("failed to decode opaque blob: %w", err)
	}
	if len(opaqueBlob) == 0 {
		return opaqueUnwrapped{}, fmt.Errorf("missing opaque blob")
	}
	if len(opaqueBlob) < challengeLen {
		return opaqueUnwrapped{}, fmt.Errorf("opaque blob too short")
	}

	// The form of the opaque blob is: <varint timeBytesLen><timeBytes><challenge><signature>
	timeBytesLen, n := binary.Uvarint(opaqueBlob)
	if n <= 0 {
		return opaqueUnwrapped{}, fmt.Errorf("failed to read timeBytesLen")
	}
	fullPayload := opaqueBlob // Store the full payload so we can verify the signature
	opaqueBlob = opaqueBlob[n:]
	timeBytes := opaqueBlob[:timeBytesLen]
	createdTime := time.Time{}
	err = createdTime.UnmarshalBinary(timeBytes)
	if err != nil {
		return opaqueUnwrapped{}, fmt.Errorf("failed to unmarshal time: %w", err)
	}
	opaqueBlob = opaqueBlob[timeBytesLen:]
	challenge := opaqueBlob[:challengeLen]
	opaqueBlob = opaqueBlob[challengeLen:]
	sig := opaqueBlob
	payloadWithoutSig := fullPayload[:len(fullPayload)-len(opaqueBlob)]
	ok, err := privKey.GetPublic().Verify(payloadWithoutSig, sig)
	if err != nil {
		return opaqueUnwrapped{}, fmt.Errorf("signature verification failed: %w", err)
	}
	if !ok {
		return opaqueUnwrapped{}, fmt.Errorf("signature verification failed")
	}
	challengeCopy := make([]byte, challengeLen) // Copy the challenge because the underlying buffer will be returned to the pool
	copy(challengeCopy, challenge)
	return opaqueUnwrapped{
		challenge:   challengeCopy,
		createdTime: createdTime,
	}, nil
}

func genOpaqueFromChallenge(buf []byte, now time.Time, privKey crypto.PrivKey, challenge []byte) ([]byte, error) {
	timeBytes, err := now.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal time: %w", err)
	}
	buf = binary.AppendUvarint(buf, uint64(len(timeBytes)))
	buf = append(buf, timeBytes...)
	buf = append(buf, challenge...)
	sig, err := privKey.Sign(buf)
	if err != nil {
		return nil, fmt.Errorf("failed to sign challenge: %w", err)
	}
	buf = append(buf, sig...)
	return buf, nil
}

func (a *ServerPeerIDAuth) serveAuthReq(w http.ResponseWriter) {
	var challenge [challengeLen]byte
	_, err := rand.Read(challenge[:])
	if err != nil {
		log.Warnf("failed to generate challenge: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
	}

	tmp := pool.Get(2048)
	defer pool.Put(tmp)
	opaque, err := genOpaqueFromChallenge(tmp[:0], time.Now(), a.PrivKey, challenge[:])
	if err != nil {
		log.Warnf("failed to generate opaque: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	authHeaderVal := pool.Get(2048)
	defer pool.Put(authHeaderVal)
	authHeaderVal = authHeaderVal[:0]
	authHeaderVal = append(authHeaderVal, serverAuthPrefix...)
	authHeaderVal = b64AppendEncode(authHeaderVal, challenge[:])
	authHeaderVal = append(authHeaderVal, ", opaque="...)
	authHeaderVal = b64AppendEncode(authHeaderVal, opaque)

	w.Header().Set("WWW-Authenticate", string(authHeaderVal))
	w.WriteHeader(http.StatusUnauthorized)
}
