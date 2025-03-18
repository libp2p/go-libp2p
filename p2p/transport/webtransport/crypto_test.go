package libp2pwebtransport

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	mrand "math/rand"
	"testing"
	"time"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/multiformats/go-multihash"
	"github.com/stretchr/testify/require"
)

func sha256Multihash(t *testing.T, b []byte) multihash.DecodedMultihash {
	t.Helper()
	hash := sha256.Sum256(b)
	h, err := multihash.Encode(hash[:], multihash.SHA2_256)
	require.NoError(t, err)
	dh, err := multihash.Decode(h)
	require.NoError(t, err)
	return *dh
}

func generateCertWithKey(t *testing.T, key crypto.PrivateKey, start, end time.Time) *x509.Certificate {
	t.Helper()
	serial := int64(mrand.Uint64())
	if serial < 0 {
		serial = -serial
	}
	certTempl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{},
		NotBefore:             start,
		NotAfter:              end,
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caBytes, err := x509.CreateCertificate(rand.Reader, certTempl, certTempl, key.(interface{ Public() crypto.PublicKey }).Public(), key)
	require.NoError(t, err)
	ca, err := x509.ParseCertificate(caBytes)
	require.NoError(t, err)
	return ca
}

func TestCertificateVerification(t *testing.T) {
	now := time.Now()
	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)

	t.Run("accepting a valid cert", func(t *testing.T) {
		validCert := generateCertWithKey(t, ecdsaKey, now, now.Add(14*24*time.Hour))
		require.NoError(t, verifyRawCerts([][]byte{validCert.Raw}, []multihash.DecodedMultihash{sha256Multihash(t, validCert.Raw)}))
	})

	for _, tc := range [...]struct {
		name   string
		cert   *x509.Certificate
		errStr string
	}{
		{
			name:   "validitity period too long",
			cert:   generateCertWithKey(t, ecdsaKey, now, now.Add(15*24*time.Hour)),
			errStr: "cert must not be valid for longer than 14 days",
		},
		{
			name:   "uses RSA key",
			cert:   generateCertWithKey(t, rsaKey, now, now.Add(14*24*time.Hour)),
			errStr: "RSA",
		},
		{
			name:   "expired certificate",
			cert:   generateCertWithKey(t, ecdsaKey, now.Add(-14*24*time.Hour), now),
			errStr: "cert not valid",
		},
		{
			name:   "not yet valid",
			cert:   generateCertWithKey(t, ecdsaKey, now.Add(time.Hour), now.Add(time.Hour+14*24*time.Hour)),
			errStr: "cert not valid",
		},
	} {
		t.Run(fmt.Sprintf("rejecting invalid certificates: %s", tc.name), func(t *testing.T) {
			err := verifyRawCerts([][]byte{tc.cert.Raw}, []multihash.DecodedMultihash{sha256Multihash(t, tc.cert.Raw)})
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errStr)
		})
	}

	for _, tc := range [...]struct {
		name   string
		certs  [][]byte
		hashes []multihash.DecodedMultihash
		errStr string
	}{
		{
			name:   "no certificates",
			hashes: []multihash.DecodedMultihash{sha256Multihash(t, []byte("foobar"))},
			errStr: "no cert",
		},
		{
			name:   "certificate not parseable",
			certs:  [][]byte{[]byte("foobar")},
			hashes: []multihash.DecodedMultihash{sha256Multihash(t, []byte("foobar"))},
			errStr: "x509: malformed certificate",
		},
		{
			name:   "hash mismatch",
			certs:  [][]byte{generateCertWithKey(t, ecdsaKey, now, now.Add(15*24*time.Hour)).Raw},
			hashes: []multihash.DecodedMultihash{sha256Multihash(t, []byte("foobar"))},
			errStr: "cert hash not found",
		},
	} {
		t.Run(fmt.Sprintf("rejecting invalid certificates: %s", tc.name), func(t *testing.T) {
			err := verifyRawCerts(tc.certs, tc.hashes)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errStr)
		})
	}
}

func TestDeterministicCertHashes(t *testing.T) {
	// Run this test 1000 times since we want to make sure the signatures are deterministic
	runs := 1000
	for i := 0; i < runs; i++ {
		zeroSeed := [32]byte{}
		priv, _, err := ic.GenerateEd25519Key(bytes.NewReader(zeroSeed[:]))
		require.NoError(t, err)
		cert, certPriv, err := generateCert(priv, time.Time{}, time.Time{}.Add(time.Hour*24*14))
		require.NoError(t, err)

		keyBytes, err := x509.MarshalECPrivateKey(certPriv)
		require.NoError(t, err)

		cert2, certPriv2, err := generateCert(priv, time.Time{}, time.Time{}.Add(time.Hour*24*14))
		require.NoError(t, err)

		require.Equal(t, cert2.Signature, cert.Signature)
		require.Equal(t, cert2.Raw, cert.Raw)
		keyBytes2, err := x509.MarshalECPrivateKey(certPriv2)
		require.NoError(t, err)
		require.Equal(t, keyBytes, keyBytes2)
	}
}

// TestDeterministicSig tests that our hack around making ECDSA signatures
// deterministic works. If this fails, this means we need to try another
// strategy to make deterministic signatures or try something else entirely.
// See deterministicReader for more context.
func TestDeterministicSig(t *testing.T) {
	// Run this test 1000 times since we want to make sure the signatures are deterministic
	runs := 1000
	for i := 0; i < runs; i++ {
		zeroSeed := [32]byte{}
		deterministicHKDFReader := newDeterministicReader(zeroSeed[:], nil, deterministicCertInfo)
		b := [1024]byte{}
		io.ReadFull(deterministicHKDFReader, b[:])
		caPrivateKey, err := ecdsa.GenerateKey(elliptic.P256(), deterministicHKDFReader)
		require.NoError(t, err)

		sig, err := caPrivateKey.Sign(deterministicHKDFReader, b[:], crypto.SHA256)
		require.NoError(t, err)

		deterministicHKDFReader = newDeterministicReader(zeroSeed[:], nil, deterministicCertInfo)
		b2 := [1024]byte{}
		io.ReadFull(deterministicHKDFReader, b2[:])
		caPrivateKey2, err := ecdsa.GenerateKey(elliptic.P256(), deterministicHKDFReader)
		require.NoError(t, err)

		sig2, err := caPrivateKey2.Sign(deterministicHKDFReader, b2[:], crypto.SHA256)
		require.NoError(t, err)

		keyBytes, err := x509.MarshalECPrivateKey(caPrivateKey)
		require.NoError(t, err)
		keyBytes2, err := x509.MarshalECPrivateKey(caPrivateKey2)
		require.NoError(t, err)

		require.Equal(t, sig, sig2)
		require.Equal(t, keyBytes, keyBytes2)
	}
}
