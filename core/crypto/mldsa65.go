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

// MLDSA65PrivateKey is a ML-DSA-65 private key.
type MLDSA65PrivateKey struct {
	k sign.PrivateKey
}

// MLDSA65PublicKey is a ML-DSA-65 public key.
type MLDSA65PublicKey struct {
	k sign.PublicKey
}

// GenerateMLDSA65Key generates a new ML-DSA-65 private and public key pair.
// Note: The src io.Reader parameter is accepted for API compatibility but is currently
// ignored because CIRCL's schemes.GenerateKey() takes no RNG parameter; crypto/rand is
// used internally by CIRCL.
func GenerateMLDSA65Key(src io.Reader) (PrivKey, PubKey, error) {
	scheme := schemes.ByName("ML-DSA-65")
	if scheme == nil {
		return nil, nil, errors.New("ML-DSA-65 scheme not available")
	}

	pub, priv, err := scheme.GenerateKey()
	if err != nil {
		return nil, nil, err
	}

	return &MLDSA65PrivateKey{
			k: priv,
		},
		&MLDSA65PublicKey{
			k: pub,
		},
		nil
}

// Type of the private key (ML-DSA-65).
func (k *MLDSA65PrivateKey) Type() pb.KeyType {
	return pb.KeyType_MLDSA65
}

// Raw private key bytes.
func (k *MLDSA65PrivateKey) Raw() ([]byte, error) {
	return k.k.MarshalBinary()
}

// Equals compares two ML-DSA-65 private keys.
func (k *MLDSA65PrivateKey) Equals(o Key) bool {
	dk, ok := o.(*MLDSA65PrivateKey)
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

// GetPublic returns a ML-DSA-65 public key from a private key.
func (k *MLDSA65PrivateKey) GetPublic() PubKey {
	return &MLDSA65PublicKey{k: k.k.Public().(sign.PublicKey)}
}

// Sign returns a signature from an input message.
func (k *MLDSA65PrivateKey) Sign(msg []byte) (res []byte, err error) {
	defer func() { catch.HandlePanic(recover(), &err, "mldsa65 signing") }()

	scheme := schemes.ByName("ML-DSA-65")
	if scheme == nil {
		return nil, errors.New("ML-DSA-65 scheme not available")
	}

	opts := &sign.SignatureOpts{}
	return scheme.Sign(k.k, msg, opts), nil
}

// Type of the public key (ML-DSA-65).
func (k *MLDSA65PublicKey) Type() pb.KeyType {
	return pb.KeyType_MLDSA65
}

// Raw public key bytes.
func (k *MLDSA65PublicKey) Raw() ([]byte, error) {
	return k.k.MarshalBinary()
}

// Equals compares two ML-DSA-65 public keys.
func (k *MLDSA65PublicKey) Equals(o Key) bool {
	dk, ok := o.(*MLDSA65PublicKey)
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
func (k *MLDSA65PublicKey) Verify(data []byte, sig []byte) (success bool, err error) {
	defer func() {
		catch.HandlePanic(recover(), &err, "mldsa65 signature verification")

		// To be safe.
		if err != nil {
			success = false
		}
	}()

	scheme := schemes.ByName("ML-DSA-65")
	if scheme == nil {
		return false, errors.New("ML-DSA-65 scheme not available")
	}

	opts := &sign.SignatureOpts{}
	return scheme.Verify(k.k, data, sig, opts), nil
}

// UnmarshalMLDSA65PublicKey returns a public key from input bytes.
func UnmarshalMLDSA65PublicKey(data []byte) (PubKey, error) {
	scheme := schemes.ByName("ML-DSA-65")
	if scheme == nil {
		return nil, errors.New("ML-DSA-65 scheme not available")
	}

	pub, err := scheme.UnmarshalBinaryPublicKey(data)
	if err != nil {
		return nil, err
	}

	return &MLDSA65PublicKey{
		k: pub,
	}, nil
}

// UnmarshalMLDSA65PrivateKey returns a private key from input bytes.
func UnmarshalMLDSA65PrivateKey(data []byte) (PrivKey, error) {
	scheme := schemes.ByName("ML-DSA-65")
	if scheme == nil {
		return nil, errors.New("ML-DSA-65 scheme not available")
	}

	priv, err := scheme.UnmarshalBinaryPrivateKey(data)
	if err != nil {
		return nil, err
	}

	return &MLDSA65PrivateKey{
		k: priv,
	}, nil
}
