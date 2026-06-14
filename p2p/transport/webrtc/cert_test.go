package libp2pwebrtc

import (
	"bytes"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"

	"github.com/stretchr/testify/require"
)

// A fixed host key must always map to this exact /certhash. The fingerprint is
// SHA-256 over the cert DER, so it is the value other peers cache and pin. This
// vector catches any change in the derivation (HKDF, filippo.io/keygen, the
// x509 encoder, or this code) that would silently rotate every node's
// webrtc-direct multiaddr. filippo.io/keygen in particular documents that its
// output may change before v1.0.0.
//
// Updating this value is a breaking, network-visible event: do not do it to
// make a red test green without understanding why the derivation changed.
func TestDeterministicCertificateGoldenVector(t *testing.T) {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i)
	}
	privKey, _, err := crypto.GenerateEd25519Key(bytes.NewReader(seed))
	require.NoError(t, err)

	cert, err := newDeterministicCertificate(privKey)
	require.NoError(t, err)

	fps, err := cert.GetFingerprints()
	require.NoError(t, err)
	require.Equal(t, "sha-256", fps[0].Algorithm)
	require.Equal(t,
		"dd:6e:2d:63:b0:f0:61:9e:18:7a:03:1f:64:77:16:37:a0:41:8a:71:dc:d8:b9:d7:8c:6c:69:b1:f4:25:64:91",
		fps[0].Value,
	)
}

// Same host key, same cert, same /certhash. This is the property restart
// stability depends on.
func TestDeterministicCertificateIsStableForSameKey(t *testing.T) {
	privKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	require.NoError(t, err)

	c1, err := newDeterministicCertificate(privKey)
	require.NoError(t, err)
	c2, err := newDeterministicCertificate(privKey)
	require.NoError(t, err)

	require.True(t, c1.Equals(*c2), "two derivations from the same host key produced different certs")

	fp1, err := c1.GetFingerprints()
	require.NoError(t, err)
	fp2, err := c2.GetFingerprints()
	require.NoError(t, err)
	require.Equal(t, fp1, fp2)

	// PEM wraps the DER bytes, so equal PEMs prove every input to /certhash
	// (cert template, public key, and signature) is byte-stable.
	pem1, err := c1.PEM()
	require.NoError(t, err)
	pem2, err := c2.PEM()
	require.NoError(t, err)
	require.Equal(t, pem1, pem2)
}

// Different host keys must yield different /certhash values, or the
// derivation has collapsed to a constant.
func TestDeterministicCertificateDiffersBetweenKeys(t *testing.T) {
	k1, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	require.NoError(t, err)
	k2, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	require.NoError(t, err)

	c1, err := newDeterministicCertificate(k1)
	require.NoError(t, err)
	c2, err := newDeterministicCertificate(k2)
	require.NoError(t, err)

	fp1, err := c1.GetFingerprints()
	require.NoError(t, err)
	fp2, err := c2.GetFingerprints()
	require.NoError(t, err)
	require.NotEqual(t, fp1[0].Value, fp2[0].Value)
}

// /certhash is sha256(DER), so the DER bytes need to be byte-stable across
// runs. A non-deterministic ECDSA nonce, an unstable x509 field, or hidden
// entropy in the cert template would all break this. Looping catches a flaky
// entropy source that might pass a one-shot check.
func TestDeterministicCertificateDERIsStable(t *testing.T) {
	privKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	require.NoError(t, err)

	first, err := newDeterministicCertificate(privKey)
	require.NoError(t, err)
	firstPEM, err := first.PEM()
	require.NoError(t, err)

	for i := range 16 {
		next, err := newDeterministicCertificate(privKey)
		require.NoError(t, err)
		nextPEM, err := next.PEM()
		require.NoError(t, err)
		require.Equal(t, firstPEM, nextPEM, "DER changed across calls (iteration %d)", i)
	}
}

// Pion rejects a cert whose NotAfter is in the past when handed to
// PeerConnection. The hardcoded window has to stay valid for the foreseeable
// life of go-libp2p.
func TestDeterministicCertificateNotExpired(t *testing.T) {
	privKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	require.NoError(t, err)

	cert, err := newDeterministicCertificate(privKey)
	require.NoError(t, err)

	require.False(t, cert.Expires().IsZero())
	require.True(t, cert.Expires().Year() >= 2100, "NotAfter must stay valid for the foreseeable life of go-libp2p")
}
