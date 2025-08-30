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

// Dilithium3PrivateKey is a Dilithium3 private key.
type Dilithium3PrivateKey struct {
	k sign.PrivateKey
}

// Dilithium3PublicKey is a Dilithium3 public key.
type Dilithium3PublicKey struct {
	k sign.PublicKey
}

// GenerateDilithium3Key generates a new Dilithium3 private and public key pair.
// Note: The src io.Reader parameter is accepted for API compatibility but is currently
// ignored because CIRCL's schemes.GenerateKey() takes no RNG parameter; crypto/rand is
// used internally by CIRCL.
func GenerateDilithium3Key(src io.Reader) (PrivKey, PubKey, error) {
	scheme := schemes.ByName("Dilithium3")
	if scheme == nil {
		return nil, nil, errors.New("Dilithium3 scheme not available")
	}

	pub, priv, err := scheme.GenerateKey()
	if err != nil {
		return nil, nil, err
	}

	return &Dilithium3PrivateKey{
			k: priv,
		},
		&Dilithium3PublicKey{
			k: pub,
		},
		nil
}

// Type of the private key (Dilithium3).
func (k *Dilithium3PrivateKey) Type() pb.KeyType {
	return pb.KeyType_Dilithium3
}

// Raw private key bytes.
func (k *Dilithium3PrivateKey) Raw() ([]byte, error) {
	return k.k.MarshalBinary()
}

// Equals compares two Dilithium3 private keys.
func (k *Dilithium3PrivateKey) Equals(o Key) bool {
	dk, ok := o.(*Dilithium3PrivateKey)
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

// GetPublic returns a Dilithium3 public key from a private key.
func (k *Dilithium3PrivateKey) GetPublic() PubKey {
	return &Dilithium3PublicKey{k: k.k.Public().(sign.PublicKey)}
}

// Sign returns a signature from an input message.
func (k *Dilithium3PrivateKey) Sign(msg []byte) (res []byte, err error) {
	defer func() { catch.HandlePanic(recover(), &err, "dilithium3 signing") }()

	scheme := schemes.ByName("Dilithium3")
	if scheme == nil {
		return nil, errors.New("Dilithium3 scheme not available")
	}

	opts := &sign.SignatureOpts{}
	return scheme.Sign(k.k, msg, opts), nil
}

// Type of the public key (Dilithium3).
func (k *Dilithium3PublicKey) Type() pb.KeyType {
	return pb.KeyType_Dilithium3
}

// Raw public key bytes.
func (k *Dilithium3PublicKey) Raw() ([]byte, error) {
	return k.k.MarshalBinary()
}

// Equals compares two Dilithium3 public keys.
func (k *Dilithium3PublicKey) Equals(o Key) bool {
	dk, ok := o.(*Dilithium3PublicKey)
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
func (k *Dilithium3PublicKey) Verify(data []byte, sig []byte) (success bool, err error) {
	defer func() {
		catch.HandlePanic(recover(), &err, "dilithium3 signature verification")

		// To be safe.
		if err != nil {
			success = false
		}
	}()

	scheme := schemes.ByName("Dilithium3")
	if scheme == nil {
		return false, errors.New("Dilithium3 scheme not available")
	}

	opts := &sign.SignatureOpts{}
	return scheme.Verify(k.k, data, sig, opts), nil
}

// UnmarshalDilithium3PublicKey returns a public key from input bytes.
func UnmarshalDilithium3PublicKey(data []byte) (PubKey, error) {
	scheme := schemes.ByName("Dilithium3")
	if scheme == nil {
		return nil, errors.New("Dilithium3 scheme not available")
	}

	pub, err := scheme.UnmarshalBinaryPublicKey(data)
	if err != nil {
		return nil, err
	}

	return &Dilithium3PublicKey{
		k: pub,
	}, nil
}

// UnmarshalDilithium3PrivateKey returns a private key from input bytes.
func UnmarshalDilithium3PrivateKey(data []byte) (PrivKey, error) {
	scheme := schemes.ByName("Dilithium3")
	if scheme == nil {
		return nil, errors.New("Dilithium3 scheme not available")
	}

	priv, err := scheme.UnmarshalBinaryPrivateKey(data)
	if err != nil {
		return nil, err
	}

	return &Dilithium3PrivateKey{
		k: priv,
	}, nil
}
