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

// Ed448Dilithium3PrivateKey is a Ed448-Dilithium3 hybrid private key.
type Ed448Dilithium3PrivateKey struct {
	k sign.PrivateKey
}

// Ed448Dilithium3PublicKey is a Ed448-Dilithium3 hybrid public key.
type Ed448Dilithium3PublicKey struct {
	k sign.PublicKey
}

// GenerateEd448Dilithium3Key generates a new Ed448-Dilithium3 hybrid private and public key pair.
func GenerateEd448Dilithium3Key(src io.Reader) (PrivKey, PubKey, error) {
	scheme := schemes.ByName("Ed448-Dilithium3")
	if scheme == nil {
		return nil, nil, errors.New("Ed448-Dilithium3 scheme not available")
	}

	pub, priv, err := scheme.GenerateKey()
	if err != nil {
		return nil, nil, err
	}

	return &Ed448Dilithium3PrivateKey{
			k: priv,
		},
		&Ed448Dilithium3PublicKey{
			k: pub,
		},
		nil
}

// Type of the private key (Ed448-Dilithium3).
func (k *Ed448Dilithium3PrivateKey) Type() pb.KeyType {
	return pb.KeyType_Ed448Dilithium3
}

// Raw private key bytes.
func (k *Ed448Dilithium3PrivateKey) Raw() ([]byte, error) {
	return k.k.MarshalBinary()
}

// Equals compares two Ed448-Dilithium3 hybrid private keys.
func (k *Ed448Dilithium3PrivateKey) Equals(o Key) bool {
	dk, ok := o.(*Ed448Dilithium3PrivateKey)
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

// GetPublic returns a Ed448-Dilithium3 hybrid public key from a private key.
func (k *Ed448Dilithium3PrivateKey) GetPublic() PubKey {
	return &Ed448Dilithium3PublicKey{k: k.k.Public().(sign.PublicKey)}
}

// Sign returns a signature from an input message.
func (k *Ed448Dilithium3PrivateKey) Sign(msg []byte) (res []byte, err error) {
	defer func() { catch.HandlePanic(recover(), &err, "ed448-dilithium3 signing") }()

	scheme := schemes.ByName("Ed448-Dilithium3")
	if scheme == nil {
		return nil, errors.New("Ed448-Dilithium3 scheme not available")
	}

	opts := &sign.SignatureOpts{}
	return scheme.Sign(k.k, msg, opts), nil
}

// Type of the public key (Ed448-Dilithium3).
func (k *Ed448Dilithium3PublicKey) Type() pb.KeyType {
	return pb.KeyType_Ed448Dilithium3
}

// Raw public key bytes.
func (k *Ed448Dilithium3PublicKey) Raw() ([]byte, error) {
	return k.k.MarshalBinary()
}

// Equals compares two Ed448-Dilithium3 hybrid public keys.
func (k *Ed448Dilithium3PublicKey) Equals(o Key) bool {
	dk, ok := o.(*Ed448Dilithium3PublicKey)
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
func (k *Ed448Dilithium3PublicKey) Verify(data []byte, sig []byte) (success bool, err error) {
	defer func() {
		catch.HandlePanic(recover(), &err, "ed448-dilithium3 signature verification")

		// To be safe.
		if err != nil {
			success = false
		}
	}()

	scheme := schemes.ByName("Ed448-Dilithium3")
	if scheme == nil {
		return false, errors.New("Ed448-Dilithium3 scheme not available")
	}

	opts := &sign.SignatureOpts{}
	return scheme.Verify(k.k, data, sig, opts), nil
}

// UnmarshalEd448Dilithium3PublicKey returns a public key from input bytes.
func UnmarshalEd448Dilithium3PublicKey(data []byte) (PubKey, error) {
	scheme := schemes.ByName("Ed448-Dilithium3")
	if scheme == nil {
		return nil, errors.New("Ed448-Dilithium3 scheme not available")
	}

	pub, err := scheme.UnmarshalBinaryPublicKey(data)
	if err != nil {
		return nil, err
	}

	return &Ed448Dilithium3PublicKey{
		k: pub,
	}, nil
}

// UnmarshalEd448Dilithium3PrivateKey returns a private key from input bytes.
func UnmarshalEd448Dilithium3PrivateKey(data []byte) (PrivKey, error) {
	scheme := schemes.ByName("Ed448-Dilithium3")
	if scheme == nil {
		return nil, errors.New("Ed448-Dilithium3 scheme not available")
	}

	priv, err := scheme.UnmarshalBinaryPrivateKey(data)
	if err != nil {
		return nil, err
	}

	return &Ed448Dilithium3PrivateKey{
		k: priv,
	}, nil
}