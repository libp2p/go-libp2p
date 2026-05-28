package libp2pwebrtc

// Deterministic webrtc-direct certificate.
//
// The DTLS cert is built from the libp2p host private key with HKDF. Same
// key in, same cert out, same /certhash every time. Restarting a node does
// not change its webrtc-direct multiaddr.
//
// Why this is safe:
//
//   - The DTLS cert is not the identity credential. Peer identity comes from
//     a Noise XX handshake run inside DTLS, with both cert fingerprints
//     bound into the Noise prologue.
//     webrtc-direct: https://github.com/libp2p/specs/blob/master/webrtc/webrtc-direct.md
//     noise: https://github.com/libp2p/specs/blob/master/noise/README.md
//
//   - The libp2p spec tells the server to disable DTLS fingerprint
//     verification, since the server does not know the client cert in
//     advance. The server therefore inspects no cert details beyond the
//     handshake.
//
//   - Browser DTLS verifies the server cert only by matching its SHA-256
//     against the SDP a=fingerprint line the browser builds from /certhash.
//     Self-signed peer certs have no trust chain; NotBefore and NotAfter are
//     not part of the check. RFC 8122 (which defines a=fingerprint) requires
//     only the fingerprint match.
//     https://datatracker.ietf.org/doc/html/rfc8122
//
//   - The 365-day validity cap in the W3C WebRTC spec applies only to certs
//     the browser generates for itself, not to the remote peer cert /certhash
//     points to.
//     https://www.w3.org/TR/webrtc/#dom-rtcpeerconnection-generatecertificate
//
//   - Pion rejects a local cert whose NotAfter has already passed when
//     constructing a PeerConnection. It does not check NotBefore. The
//     hardcoded NotAfter below stays valid into the next century.
//
//   - Browser interop is empirically robust across very different cert
//     lifetimes. js-libp2p rotates every 14 days, rust-libp2p has its own
//     scheme, and earlier go-libp2p used pion's ~30-day default. All three
//     interoperate with Chrome, Firefox, and Safari today.
//
// Compared to p2p/transport/webtransport/crypto.go, WebTransport must rotate
// every 14 days because the W3C WebTransport spec hard-caps
// serverCertificateHashes certs to under 14 days. WebRTC has no such cap, so
// the window is static and rotation is skipped.
// https://www.w3.org/TR/webtransport/#dom-webtransport-servercertificatehashes
//
// Every input to x509.CreateCertificate (serial, dates, public key, signature
// nonce) must be deterministic so the DER bytes, and therefore /certhash,
// are stable. The ECDSA key is HKDF-derived; signing goes through
// deterministicSigner, which signs with nil rand. Go 1.24+ guarantees
// deterministic ECDSA in that case.
// https://go.dev/doc/go1.24#cryptoecdsapkgcryptoecdsa
//
// If a future browser starts validating remote DTLS cert dates, add rotation
// using p2p/transport/webtransport/cert_manager.go as the template.

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"time"

	"filippo.io/keygen"
	"golang.org/x/crypto/hkdf"

	ic "github.com/libp2p/go-libp2p/core/crypto"

	"github.com/pion/webrtc/v4"
)

const deterministicCertHKDFInfo = "libp2p webrtc-direct deterministic cert"

// Fixed validity window. DER bytes must be identical across runs, so the
// dates cannot depend on time.Now(). A century is well beyond any realistic
// deployment horizon and any pion NotAfter check.
var (
	deterministicCertNotBefore = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	deterministicCertNotAfter  = time.Date(2120, 1, 1, 0, 0, 0, 0, time.UTC)
)

// newDeterministicCertificate returns a webrtc.Certificate whose SHA-256
// fingerprint (the /certhash component) depends only on the host private
// key. The cert uses ECDSA P-256, the only ECDSA curve Chromium accepts in
// WebRTC DTLS; RSA works but produces larger, slower certs.
func newDeterministicCertificate(key ic.PrivKey) (*webrtc.Certificate, error) {
	keyBytes, err := key.Raw()
	if err != nil {
		return nil, fmt.Errorf("read host key bytes: %w", err)
	}
	r := hkdf.New(sha256.New, keyBytes, nil, []byte(deterministicCertHKDFInfo))

	serialBytes := make([]byte, 8)
	if _, err := io.ReadFull(r, serialBytes); err != nil {
		return nil, fmt.Errorf("derive cert serial: %w", err)
	}
	serial := int64(binary.BigEndian.Uint64(serialBytes))
	if serial < 0 {
		serial = -serial
	}

	ecdsaSeed := make([]byte, 192/8) // 192 bits of entropy for P-256, per filippo.io/keygen
	if _, err := io.ReadFull(r, ecdsaSeed); err != nil {
		return nil, fmt.Errorf("derive ECDSA seed: %w", err)
	}
	priv, err := keygen.ECDSA(elliptic.P256(), ecdsaSeed)
	if err != nil {
		return nil, fmt.Errorf("derive ECDSA key: %w", err)
	}

	tpl := &x509.Certificate{
		SerialNumber:       big.NewInt(serial),
		Subject:            pkix.Name{CommonName: "libp2p-webrtc-direct"},
		Issuer:             pkix.Name{CommonName: "libp2p-webrtc-direct"},
		NotBefore:          deterministicCertNotBefore,
		NotAfter:           deterministicCertNotAfter,
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}

	// x509.CreateCertificate forwards its rand reader to the signer for the
	// ECDSA nonce. deterministicSigner ignores that reader and signs with nil,
	// which Go 1.24+ guarantees is deterministic. The HKDF stream is passed in
	// as belt-and-braces, in case CreateCertificate ever reads rand for
	// something else.
	der, err := x509.CreateCertificate(r, tpl, tpl, priv.Public(), deterministicSigner{priv})
	if err != nil {
		return nil, fmt.Errorf("create x509 certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse generated certificate: %w", err)
	}

	wrapped := webrtc.CertificateFromX509(priv, cert)
	return &wrapped, nil
}

// deterministicSigner wraps an ecdsa.PrivateKey so the cert signature is
// reproducible. Signing with rand=nil triggers Go 1.24+'s deterministic
// ECDSA path:
// https://go.dev/doc/go1.24#cryptoecdsapkgcryptoecdsa
type deterministicSigner struct {
	priv *ecdsa.PrivateKey
}

var _ crypto.Signer = deterministicSigner{}

func (ds deterministicSigner) Public() crypto.PublicKey { return ds.priv.Public() }

func (ds deterministicSigner) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	return ds.priv.Sign(nil, digest, opts)
}
