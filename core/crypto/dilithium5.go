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

// Dilithium5PrivateKey is a Dilithium5 private key.
type Dilithium5PrivateKey struct {
	k sign.PrivateKey
}

// Dilithium5PublicKey is a Dilithium5 public key.
type Dilithium5PublicKey struct {
	k sign.PublicKey
}

// GenerateDilithium5Key generates a new Dilithium5 private and public key pair.
func GenerateDilithium5Key(src io.Reader) (PrivKey, PubKey, error) {
	scheme := schemes.ByName("Dilithium5")
	if scheme == nil {
		return nil, nil, errors.New("Dilithium5 scheme not available")
	}

	pub, priv, err := scheme.GenerateKey()
	if err != nil {
		return nil, nil, err
	}

	return &Dilithium5PrivateKey{
			k: priv,
		},
		&Dilithium5PublicKey{
			k: pub,
		},
		nil
}

// Type of the private key (Dilithium5).
func (k *Dilithium5PrivateKey) Type() pb.KeyType {
	return pb.KeyType_Dilithium5
}

// Raw private key bytes.
func (k *Dilithium5PrivateKey) Raw() ([]byte, error) {
	return k.k.MarshalBinary()
}

// Equals compares two Dilithium5 private keys.
func (k *Dilithium5PrivateKey) Equals(o Key) bool {
	dk, ok := o.(*Dilithium5PrivateKey)
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

// GetPublic returns a Dilithium5 public key from a private key.
func (k *Dilithium5PrivateKey) GetPublic() PubKey {
	return &Dilithium5PublicKey{k: k.k.Public().(sign.PublicKey)}
}

// Sign returns a signature from an input message.
func (k *Dilithium5PrivateKey) Sign(msg []byte) (res []byte, err error) {
	defer func() { catch.HandlePanic(recover(), &err, "dilithium5 signing") }()

	scheme := schemes.ByName("Dilithium5")
	if scheme == nil {
		return nil, errors.New("Dilithium5 scheme not available")
	}

	opts := &sign.SignatureOpts{}
	return scheme.Sign(k.k, msg, opts), nil
}

// Type of the public key (Dilithium5).
func (k *Dilithium5PublicKey) Type() pb.KeyType {
	return pb.KeyType_Dilithium5
}

// Raw public key bytes.
func (k *Dilithium5PublicKey) Raw() ([]byte, error) {
	return k.k.MarshalBinary()
}

// Equals compares two Dilithium5 public keys.
func (k *Dilithium5PublicKey) Equals(o Key) bool {
	dk, ok := o.(*Dilithium5PublicKey)
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
func (k *Dilithium5PublicKey) Verify(data []byte, sig []byte) (success bool, err error) {
	defer func() {
		catch.HandlePanic(recover(), &err, "dilithium5 signature verification")

		// To be safe.
		if err != nil {
			success = false
		}
	}()

	scheme := schemes.ByName("Dilithium5")
	if scheme == nil {
		return false, errors.New("Dilithium5 scheme not available")
	}

	opts := &sign.SignatureOpts{}
	return scheme.Verify(k.k, data, sig, opts), nil
}

// UnmarshalDilithium5PublicKey returns a public key from input bytes.
func UnmarshalDilithium5PublicKey(data []byte) (PubKey, error) {
	scheme := schemes.ByName("Dilithium5")
	if scheme == nil {
		return nil, errors.New("Dilithium5 scheme not available")
	}

	pub, err := scheme.UnmarshalBinaryPublicKey(data)
	if err != nil {
		return nil, err
	}

	return &Dilithium5PublicKey{
		k: pub,
	}, nil
}

// UnmarshalDilithium5PrivateKey returns a private key from input bytes.
func UnmarshalDilithium5PrivateKey(data []byte) (PrivKey, error) {
	scheme := schemes.ByName("Dilithium5")
	if scheme == nil {
		return nil, errors.New("Dilithium5 scheme not available")
	}

	priv, err := scheme.UnmarshalBinaryPrivateKey(data)
	if err != nil {
		return nil, err
	}

	return &Dilithium5PrivateKey{
		k: priv,
	}, nil
}