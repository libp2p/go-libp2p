package crypto

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"io"

	pb "github.com/libp2p/go-libp2p/core/crypto/pb"
	"github.com/cloudflare/circl/sign"
	"github.com/cloudflare/circl/sign/schemes"
	"github.com/libp2p/go-libp2p/core/internal/catch"
)

// MLDSA44PrivateKey is a ML-DSA-44 private key.
type MLDSA44PrivateKey struct {
	k sign.PrivateKey
}

// MLDSA44PublicKey is a ML-DSA-44 public key.
type MLDSA44PublicKey struct {
	k sign.PublicKey
}

// GenerateMLDSA44Key generates a new ML-DSA-44 private and public key pair.
func GenerateMLDSA44Key(src io.Reader) (PrivKey, PubKey, error) {
	scheme := schemes.ByName("ML-DSA-44")
	if scheme == nil {
		return nil, nil, errors.New("ML-DSA-44 scheme not available")
	}

	pub, priv, err := scheme.GenerateKey()
	if err != nil {
		return nil, nil, err
	}

	return &MLDSA44PrivateKey{
			k: priv,
		},
		&MLDSA44PublicKey{
			k: pub,
		},
		nil
}

// Type of the private key (ML-DSA-44).
func (k *MLDSA44PrivateKey) Type() pb.KeyType {
	return pb.KeyType_MLDSA44
}

// Raw private key bytes.
func (k *MLDSA44PrivateKey) Raw() ([]byte, error) {
	return k.k.MarshalBinary()
}

// Equals compares two ML-DSA-44 private keys.
func (k *MLDSA44PrivateKey) Equals(o Key) bool {
	dk, ok := o.(*MLDSA44PrivateKey)
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

// GetPublic returns a ML-DSA-44 public key from a private key.
func (k *MLDSA44PrivateKey) GetPublic() PubKey {
	return &MLDSA44PublicKey{k: k.k.Public().(sign.PublicKey)}
}

// Sign returns a signature from an input message.
func (k *MLDSA44PrivateKey) Sign(msg []byte) (res []byte, err error) {
	defer func() { catch.HandlePanic(recover(), &err, "mldsa44 signing") }()

	scheme := schemes.ByName("ML-DSA-44")
	if scheme == nil {
		return nil, errors.New("ML-DSA-44 scheme not available")
	}

	opts := &sign.SignatureOpts{}
	return scheme.Sign(k.k, msg, opts), nil
}

// Type of the public key (ML-DSA-44).
func (k *MLDSA44PublicKey) Type() pb.KeyType {
	return pb.KeyType_MLDSA44
}

// Raw public key bytes.
func (k *MLDSA44PublicKey) Raw() ([]byte, error) {
	return k.k.MarshalBinary()
}

// Equals compares two ML-DSA-44 public keys.
func (k *MLDSA44PublicKey) Equals(o Key) bool {
	dk, ok := o.(*MLDSA44PublicKey)
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
func (k *MLDSA44PublicKey) Verify(data []byte, sig []byte) (success bool, err error) {
	defer func() {
		catch.HandlePanic(recover(), &err, "mldsa44 signature verification")

		// To be safe.
		if err != nil {
			success = false
		}
	}()

	scheme := schemes.ByName("ML-DSA-44")
	if scheme == nil {
		return false, errors.New("ML-DSA-44 scheme not available")
	}

	opts := &sign.SignatureOpts{}
	return scheme.Verify(k.k, data, sig, opts), nil
}

// UnmarshalMLDSA44PublicKey returns a public key from input bytes.
func UnmarshalMLDSA44PublicKey(data []byte) (PubKey, error) {
	scheme := schemes.ByName("ML-DSA-44")
	if scheme == nil {
		return nil, errors.New("ML-DSA-44 scheme not available")
	}

	pub, err := scheme.UnmarshalBinaryPublicKey(data)
	if err != nil {
		return nil, err
	}

	return &MLDSA44PublicKey{
		k: pub,
	}, nil
}

// UnmarshalMLDSA44PrivateKey returns a private key from input bytes.
func UnmarshalMLDSA44PrivateKey(data []byte) (PrivKey, error) {
	scheme := schemes.ByName("ML-DSA-44")
	if scheme == nil {
		return nil, errors.New("ML-DSA-44 scheme not available")
	}

	priv, err := scheme.UnmarshalBinaryPrivateKey(data)
	if err != nil {
		return nil, err
	}

	return &MLDSA44PrivateKey{
		k: priv,
	}, nil
}