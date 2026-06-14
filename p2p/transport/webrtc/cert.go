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
// The serial/key derivation and deterministicSigner below are deliberately
// kept local to this transport rather than shared with webtransport, even
// though the two look similar today. WebRTC and WebTransport are independent on
// the wire and in their browser implementations, and their cert requirements
// may diverge; keeping each self-contained avoids coupling one transport's
// behavior to the other.
//
// Every input to x509.CreateCertificate (serial, dates, public key, signature
// nonce) must be deterministic so the DER bytes, and therefore /certhash, are
// stable. The ECDSA key is HKDF-derived; signing goes through
// deterministicSigner, which signs with nil rand, the Go 1.24+ deterministic
// ECDSA path.
// https://go.dev/doc/go1.24#cryptoecdsapkgcryptoecdsa
//
// DER byte-stability spans a single Go toolchain plus a fixed set of these
// dependencies (filippo.io/keygen in particular documents that its output may
// change before v1.0.0). TestDeterministicCertificateGoldenVector pins the
// fingerprint for a fixed key so any keygen, bigmod, or x509 encoding change
// that would rotate every node's /certhash fails CI instead of shipping
// silently. Changing that golden value is a breaking, network-visible event.
//
// If a future browser starts validating remote DTLS cert dates, add rotation
// using p2p/transport/webtransport/cert_manager.go as the template.

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"time"

	"filippo.io/keygen"

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

	// HKDF expands the host key into the serial bytes followed by the ECDSA
	// seed. The same key always yields the same bytes (RFC 5869), so every
	// derived input below is deterministic.
	const serialLen = 8
	const seedLen = 192 / 8 // 192 bits of entropy for P-256, per filippo.io/keygen
	material, err := hkdf.Key(sha256.New, keyBytes, nil, deterministicCertHKDFInfo, serialLen+seedLen)
	if err != nil {
		return nil, fmt.Errorf("derive cert material: %w", err)
	}

	priv, err := keygen.ECDSA(elliptic.P256(), material[serialLen:])
	if err != nil {
		return nil, fmt.Errorf("derive ECDSA key: %w", err)
	}

	// Read the serial bytes as unsigned so the serial is never negative:
	// x509.CreateCertificate rejects a negative SerialNumber, and an int64
	// abs() cannot fix math.MinInt64 (negating it overflows back to itself).
	// max(_, 1) keeps it positive when the bytes are all zero, as RFC 5280
	// section 4.1.2.2 requires.
	serial := new(big.Int).SetUint64(max(binary.BigEndian.Uint64(material[:serialLen]), 1))

	tpl := &x509.Certificate{
		SerialNumber:       serial,
		Subject:            pkix.Name{CommonName: "libp2p-webrtc-direct"},
		Issuer:             pkix.Name{CommonName: "libp2p-webrtc-direct"},
		NotBefore:          deterministicCertNotBefore,
		NotAfter:           deterministicCertNotAfter,
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}

	// CreateCertificate forwards rand only to the signer (the supplied non-nil
	// SerialNumber means it never draws rand for that). deterministicSigner
	// ignores the reader and signs with nil, the Go 1.24+ deterministic ECDSA
	// path, so the DER bytes are reproducible regardless of the reader here.
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, priv.Public(), deterministicSigner{priv})
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
//
// Kept local to this transport on purpose; see the file-level note above.
type deterministicSigner struct {
	priv *ecdsa.PrivateKey
}

var _ crypto.Signer = deterministicSigner{}

func (ds deterministicSigner) Public() crypto.PublicKey { return ds.priv.Public() }

func (ds deterministicSigner) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	return ds.priv.Sign(nil, digest, opts)
}
