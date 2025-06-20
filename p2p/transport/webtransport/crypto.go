package libp2pwebtransport

import (
	"bytes"
	"crypto"
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

	"filippo.io/keygen"

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
func generateCert(key ic.PrivKey, start, end time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	keyBytes, err := key.Raw()
	if err != nil {
		return nil, nil, err
	}

	startTimeSalt := make([]byte, 8)
	binary.LittleEndian.PutUint64(startTimeSalt, uint64(start.UnixNano()))
	deterministicHKDFReader := newDeterministicReader(keyBytes, startTimeSalt, deterministicCertInfo)

	b := make([]byte, 8)
	if _, err := deterministicHKDFReader.Read(b); err != nil {
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

	ecdsaSeed := make([]byte, 192/8) // 192 bits of entropy required for P256
	if _, err := io.ReadFull(deterministicHKDFReader, ecdsaSeed); err != nil {
		return nil, nil, err
	}

	caPrivateKey, err := keygen.ECDSA(elliptic.P256(), ecdsaSeed)
	if err != nil {
		return nil, nil, err
	}
	caBytes, err := x509.CreateCertificate(deterministicHKDFReader, certTempl, certTempl, caPrivateKey.Public(), deterministicSigner{caPrivateKey})
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

func newDeterministicReader(seed []byte, salt []byte, info string) io.Reader {
	return hkdf.New(sha256.New, seed, salt, []byte(info))
}

// deterministicSigner wraps an ecdsa.PrivateKey and exposes a `Sign` method
// that will produce deterministic signatures by ignoring the rand reader.
// Go 1.24 produces deterministic ecdsa signatures when passed a nil random source.
// See: https://go.dev/doc/go1.24#cryptoecdsapkgcryptoecdsa
type deterministicSigner struct {
	priv *ecdsa.PrivateKey
}

var _ crypto.Signer = deterministicSigner{}

func (ds deterministicSigner) Sign(rand io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	// Ignore the rand reader to produce deterministic signatures.
	_ = rand
	return ds.priv.Sign(nil, digest, opts)
}

func (ds deterministicSigner) Public() crypto.PublicKey {
	return ds.priv.Public()
}
