package libp2pwebtransport

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"time"

	"golang.org/x/crypto/hkdf"

	ic "github.com/libp2p/go-libp2p/core/crypto"

	"github.com/multiformats/go-multihash"
	"github.com/quic-go/quic-go/http3"
)

const deterministicCertInfo = "determinisitic cert"

func getTLSConf(key ic.PrivKey, start, end time.Time) (*tls.Config, error) {
	cert, priv, err := generateCert(key, start, end)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{cert.Raw},
			PrivateKey:  priv,
			Leaf:        cert,
		}},
		NextProtos: []string{http3.NextProtoH3},
	}, nil
}

// generateCert generates certs deterministically based on the `key` and start
// time passed in. Uses `golang.org/x/crypto/hkdf`.
//
// Starting with Go 1.26, crypto/ecdsa functions ignore the io.Reader parameter
// and always use the system CSPRNG. To maintain deterministic cert generation,
// we derive the ECDSA private key bytes directly from HKDF and construct the
// key using ecdsa.ParseRawPrivateKey. Go 1.26's ECDSA signing uses RFC 6979
// deterministic nonces, so the resulting certificates are fully deterministic.
func generateCert(key ic.PrivKey, start, end time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	keyBytes, err := key.Raw()
	if err != nil {
		return nil, nil, err
	}

	startTimeSalt := make([]byte, 8)
	binary.LittleEndian.PutUint64(startTimeSalt, uint64(start.UnixNano()))
	hkdfReader := hkdf.New(sha256.New, keyBytes, startTimeSalt, []byte(deterministicCertInfo))

	b := make([]byte, 8)
	if _, err := hkdfReader.Read(b); err != nil {
		return nil, nil, err
	}
	serial := int64(binary.BigEndian.Uint64(b))
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

	// Derive ECDSA private key deterministically from HKDF output.
	// P256 private keys are 32 bytes. We use rejection sampling to ensure
	// the derived bytes form a valid scalar (non-zero, less than curve order).
	var caPrivateKey *ecdsa.PrivateKey
	for {
		scalarBytes := make([]byte, 32)
		if _, err := io.ReadFull(hkdfReader, scalarBytes); err != nil {
			return nil, nil, err
		}
		caPrivateKey, err = ecdsa.ParseRawPrivateKey(elliptic.P256(), scalarBytes)
		if err == nil {
			break
		}
		// Invalid scalar (zero or >= curve order), try next 32 bytes from HKDF.
		// The probability of this happening is negligible (~2^-128 for P256).
	}

	caBytes, err := x509.CreateCertificate(nil, certTempl, certTempl, caPrivateKey.Public(), caPrivateKey)
	if err != nil {
		return nil, nil, err
	}
	ca, err := x509.ParseCertificate(caBytes)
	if err != nil {
		return nil, nil, err
	}
	return ca, caPrivateKey, nil
}

type ErrCertHashMismatch struct {
	Expected []byte
	Actual   [][]byte
}

func (e ErrCertHashMismatch) Error() string {
	return fmt.Sprintf("cert hash not found: %x (expected: %#x)", e.Expected, e.Actual)
}

func verifyRawCerts(rawCerts [][]byte, certHashes []multihash.DecodedMultihash) error {
	if len(rawCerts) < 1 {
		return errors.New("no cert")
	}
	leaf := rawCerts[len(rawCerts)-1]
	// The W3C WebTransport specification currently only allows SHA-256 certificates for serverCertificateHashes.
	hash := sha256.Sum256(leaf)
	var verified bool
	for _, h := range certHashes {
		if h.Code == multihash.SHA2_256 && bytes.Equal(h.Digest, hash[:]) {
			verified = true
			break
		}
	}
	if !verified {
		digests := make([][]byte, 0, len(certHashes))
		for _, h := range certHashes {
			digests = append(digests, h.Digest)
		}
		return ErrCertHashMismatch{Expected: hash[:], Actual: digests}
	}

	cert, err := x509.ParseCertificate(leaf)
	if err != nil {
		return err
	}
	// TODO: is this the best (and complete?) way to identify RSA certificates?
	switch cert.SignatureAlgorithm {
	case x509.SHA1WithRSA, x509.SHA256WithRSA, x509.SHA384WithRSA, x509.SHA512WithRSA, x509.MD2WithRSA, x509.MD5WithRSA:
		return errors.New("cert uses RSA")
	}
	if l := cert.NotAfter.Sub(cert.NotBefore); l > 14*24*time.Hour {
		return fmt.Errorf("cert must not be valid for longer than 14 days (NotBefore: %s, NotAfter: %s, Length: %s)", cert.NotBefore, cert.NotAfter, l)
	}
	now := time.Now()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return fmt.Errorf("cert not valid (NotBefore: %s, NotAfter: %s)", cert.NotBefore, cert.NotAfter)
	}
	return nil
}

// newDeterministicReader returns an HKDF reader for deterministic byte derivation.
// This is used in tests that need deterministic randomness from HKDF.
func newDeterministicReader(seed []byte, salt []byte, info string) io.Reader {
	return hkdf.New(sha256.New, seed, salt, []byte(info))
}
