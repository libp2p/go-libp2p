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

// Ed25519Dilithium2PrivateKey is a Ed25519-Dilithium2 hybrid private key.
type Ed25519Dilithium2PrivateKey struct {
	k sign.PrivateKey
}

// Ed25519Dilithium2PublicKey is a Ed25519-Dilithium2 hybrid public key.
type Ed25519Dilithium2PublicKey struct {
	k sign.PublicKey
}

// GenerateEd25519Dilithium2Key generates a new Ed25519-Dilithium2 hybrid private and public key pair.
// Note: The src io.Reader parameter is accepted for API compatibility but is currently
// ignored because CIRCL's schemes.GenerateKey() takes no RNG parameter; crypto/rand is
// used internally by CIRCL.
func GenerateEd25519Dilithium2Key(src io.Reader) (PrivKey, PubKey, error) {
	scheme := schemes.ByName("Ed25519-Dilithium2")
	if scheme == nil {
		return nil, nil, errors.New("Ed25519-Dilithium2 scheme not available")
	}

	pub, priv, err := scheme.GenerateKey()
	if err != nil {
		return nil, nil, err
	}

	return &Ed25519Dilithium2PrivateKey{
			k: priv,
		},
		&Ed25519Dilithium2PublicKey{
			k: pub,
		},
		nil
}

// Type of the private key (Ed25519-Dilithium2).
func (k *Ed25519Dilithium2PrivateKey) Type() pb.KeyType {
	return pb.KeyType_Ed25519Dilithium2
}

// Raw private key bytes.
func (k *Ed25519Dilithium2PrivateKey) Raw() ([]byte, error) {
	return k.k.MarshalBinary()
}

// Equals compares two Ed25519-Dilithium2 hybrid private keys.
func (k *Ed25519Dilithium2PrivateKey) Equals(o Key) bool {
	dk, ok := o.(*Ed25519Dilithium2PrivateKey)
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

// GetPublic returns a Ed25519-Dilithium2 hybrid public key from a private key.
func (k *Ed25519Dilithium2PrivateKey) GetPublic() PubKey {
	return &Ed25519Dilithium2PublicKey{k: k.k.Public().(sign.PublicKey)}
}

// Sign returns a signature from an input message.
func (k *Ed25519Dilithium2PrivateKey) Sign(msg []byte) (res []byte, err error) {
	defer func() { catch.HandlePanic(recover(), &err, "ed25519-dilithium2 signing") }()

	scheme := schemes.ByName("Ed25519-Dilithium2")
	if scheme == nil {
		return nil, errors.New("Ed25519-Dilithium2 scheme not available")
	}

	opts := &sign.SignatureOpts{}
	return scheme.Sign(k.k, msg, opts), nil
}

// Type of the public key (Ed25519-Dilithium2).
func (k *Ed25519Dilithium2PublicKey) Type() pb.KeyType {
	return pb.KeyType_Ed25519Dilithium2
}

// Raw public key bytes.
func (k *Ed25519Dilithium2PublicKey) Raw() ([]byte, error) {
	return k.k.MarshalBinary()
}

// Equals compares two Ed25519-Dilithium2 hybrid public keys.
func (k *Ed25519Dilithium2PublicKey) Equals(o Key) bool {
	dk, ok := o.(*Ed25519Dilithium2PublicKey)
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
func (k *Ed25519Dilithium2PublicKey) Verify(data []byte, sig []byte) (success bool, err error) {
	defer func() {
		catch.HandlePanic(recover(), &err, "ed25519-dilithium2 signature verification")

		// To be safe.
		if err != nil {
			success = false
		}
	}()

	scheme := schemes.ByName("Ed25519-Dilithium2")
	if scheme == nil {
		return false, errors.New("Ed25519-Dilithium2 scheme not available")
	}

	opts := &sign.SignatureOpts{}
	return scheme.Verify(k.k, data, sig, opts), nil
}

// UnmarshalEd25519Dilithium2PublicKey returns a public key from input bytes.
func UnmarshalEd25519Dilithium2PublicKey(data []byte) (PubKey, error) {
	scheme := schemes.ByName("Ed25519-Dilithium2")
	if scheme == nil {
		return nil, errors.New("Ed25519-Dilithium2 scheme not available")
	}

	pub, err := scheme.UnmarshalBinaryPublicKey(data)
	if err != nil {
		return nil, err
	}

	return &Ed25519Dilithium2PublicKey{
		k: pub,
	}, nil
}

// UnmarshalEd25519Dilithium2PrivateKey returns a private key from input bytes.
func UnmarshalEd25519Dilithium2PrivateKey(data []byte) (PrivKey, error) {
	scheme := schemes.ByName("Ed25519-Dilithium2")
	if scheme == nil {
		return nil, errors.New("Ed25519-Dilithium2 scheme not available")
	}

	priv, err := scheme.UnmarshalBinaryPrivateKey(data)
	if err != nil {
		return nil, err
	}

	return &Ed25519Dilithium2PrivateKey{
		k: priv,
	}, nil
}
