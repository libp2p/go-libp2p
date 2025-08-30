package crypto

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"io"

	"github.com/cloudflare/circl/sign"
	"github.com/cloudflare/circl/sign/schemes"
	pb "github.com/libp2p/go-libp2p/core/crypto/pb"
	"github.com/libp2p/go-libp2p/core/internal/catch"
)

// MLDSA87PrivateKey is a ML-DSA-87 private key.
type MLDSA87PrivateKey struct {
	k sign.PrivateKey
}

// MLDSA87PublicKey is a ML-DSA-87 public key.
type MLDSA87PublicKey struct {
	k sign.PublicKey
}

// GenerateMLDSA87Key generates a new ML-DSA-87 private and public key pair.
// Note: The src io.Reader parameter is accepted for API compatibility but is currently
// ignored because CIRCL's schemes.GenerateKey() takes no RNG parameter; crypto/rand is
// used internally by CIRCL.
func GenerateMLDSA87Key(src io.Reader) (PrivKey, PubKey, error) {
	scheme := schemes.ByName("ML-DSA-87")
	if scheme == nil {
		return nil, nil, errors.New("ML-DSA-87 scheme not available")
	}

	pub, priv, err := scheme.GenerateKey()
	if err != nil {
		return nil, nil, err
	}

	return &MLDSA87PrivateKey{
			k: priv,
		},
		&MLDSA87PublicKey{
			k: pub,
		},
		nil
}

// Type of the private key (ML-DSA-87).
func (k *MLDSA87PrivateKey) Type() pb.KeyType {
	return pb.KeyType_MLDSA87
}

// Raw private key bytes.
func (k *MLDSA87PrivateKey) Raw() ([]byte, error) {
	return k.k.MarshalBinary()
}

// Equals compares two ML-DSA-87 private keys.
func (k *MLDSA87PrivateKey) Equals(o Key) bool {
	dk, ok := o.(*MLDSA87PrivateKey)
	if !ok {
		return basicEquals(k, o)
	}

	a, err := k.Raw()
	if err != nil {
		return false
	}
	b, err := dk.Raw()
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(a, b) == 1
}

// GetPublic returns a ML-DSA-87 public key from a private key.
func (k *MLDSA87PrivateKey) GetPublic() PubKey {
	return &MLDSA87PublicKey{k: k.k.Public().(sign.PublicKey)}
}

// Sign returns a signature from an input message.
func (k *MLDSA87PrivateKey) Sign(msg []byte) (res []byte, err error) {
	defer func() { catch.HandlePanic(recover(), &err, "mldsa87 signing") }()

	scheme := schemes.ByName("ML-DSA-87")
	if scheme == nil {
		return nil, errors.New("ML-DSA-87 scheme not available")
	}

	opts := &sign.SignatureOpts{}
	return scheme.Sign(k.k, msg, opts), nil
}

// Type of the public key (ML-DSA-87).
func (k *MLDSA87PublicKey) Type() pb.KeyType {
	return pb.KeyType_MLDSA87
}

// Raw public key bytes.
func (k *MLDSA87PublicKey) Raw() ([]byte, error) {
	return k.k.MarshalBinary()
}

// Equals compares two ML-DSA-87 public keys.
func (k *MLDSA87PublicKey) Equals(o Key) bool {
	dk, ok := o.(*MLDSA87PublicKey)
	if !ok {
		return basicEquals(k, o)
	}

	a, err := k.Raw()
	if err != nil {
		return false
	}
	b, err := dk.Raw()
	if err != nil {
		return false
	}
	return bytes.Equal(a, b)
}

// Verify checks a signature against the input data.
func (k *MLDSA87PublicKey) Verify(data []byte, sig []byte) (success bool, err error) {
	defer func() {
		catch.HandlePanic(recover(), &err, "mldsa87 signature verification")

		// To be safe.
		if err != nil {
			success = false
		}
	}()

	scheme := schemes.ByName("ML-DSA-87")
	if scheme == nil {
		return false, errors.New("ML-DSA-87 scheme not available")
	}

	opts := &sign.SignatureOpts{}
	return scheme.Verify(k.k, data, sig, opts), nil
}

// UnmarshalMLDSA87PublicKey returns a public key from input bytes.
func UnmarshalMLDSA87PublicKey(data []byte) (PubKey, error) {
	scheme := schemes.ByName("ML-DSA-87")
	if scheme == nil {
		return nil, errors.New("ML-DSA-87 scheme not available")
	}

	pub, err := scheme.UnmarshalBinaryPublicKey(data)
	if err != nil {
		return nil, err
	}

	return &MLDSA87PublicKey{
		k: pub,
	}, nil
}

// UnmarshalMLDSA87PrivateKey returns a private key from input bytes.
func UnmarshalMLDSA87PrivateKey(data []byte) (PrivKey, error) {
	scheme := schemes.ByName("ML-DSA-87")
	if scheme == nil {
		return nil, errors.New("ML-DSA-87 scheme not available")
	}

	priv, err := scheme.UnmarshalBinaryPrivateKey(data)
	if err != nil {
		return nil, err
	}

	return &MLDSA87PrivateKey{
		k: priv,
	}, nil
}
