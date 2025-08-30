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

// Dilithium2PrivateKey is a Dilithium2 private key.
type Dilithium2PrivateKey struct {
	k sign.PrivateKey
}

// Dilithium2PublicKey is a Dilithium2 public key.
type Dilithium2PublicKey struct {
	k sign.PublicKey
}

// GenerateDilithium2Key generates a new Dilithium2 private and public key pair.
func GenerateDilithium2Key(src io.Reader) (PrivKey, PubKey, error) {
	scheme := schemes.ByName("Dilithium2")
	if scheme == nil {
		return nil, nil, errors.New("Dilithium2 scheme not available")
	}

	pub, priv, err := scheme.GenerateKey()
	if err != nil {
		return nil, nil, err
	}

	return &Dilithium2PrivateKey{
			k: priv,
		},
		&Dilithium2PublicKey{
			k: pub,
		},
		nil
}

// Type of the private key (Dilithium2).
func (k *Dilithium2PrivateKey) Type() pb.KeyType {
	return pb.KeyType_Dilithium2
}

// Raw private key bytes.
func (k *Dilithium2PrivateKey) Raw() ([]byte, error) {
	return k.k.MarshalBinary()
}

// Equals compares two Dilithium2 private keys.
func (k *Dilithium2PrivateKey) Equals(o Key) bool {
	dk, ok := o.(*Dilithium2PrivateKey)
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

// GetPublic returns a Dilithium2 public key from a private key.
func (k *Dilithium2PrivateKey) GetPublic() PubKey {
	return &Dilithium2PublicKey{k: k.k.Public().(sign.PublicKey)}
}

// Sign returns a signature from an input message.
func (k *Dilithium2PrivateKey) Sign(msg []byte) (res []byte, err error) {
	defer func() { catch.HandlePanic(recover(), &err, "dilithium2 signing") }()

	scheme := schemes.ByName("Dilithium2")
	if scheme == nil {
		return nil, errors.New("Dilithium2 scheme not available")
	}

	opts := &sign.SignatureOpts{}
	return scheme.Sign(k.k, msg, opts), nil
}

// Type of the public key (Dilithium2).
func (k *Dilithium2PublicKey) Type() pb.KeyType {
	return pb.KeyType_Dilithium2
}

// Raw public key bytes.
func (k *Dilithium2PublicKey) Raw() ([]byte, error) {
	return k.k.MarshalBinary()
}

// Equals compares two Dilithium2 public keys.
func (k *Dilithium2PublicKey) Equals(o Key) bool {
	dk, ok := o.(*Dilithium2PublicKey)
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
func (k *Dilithium2PublicKey) Verify(data []byte, sig []byte) (success bool, err error) {
	defer func() {
		catch.HandlePanic(recover(), &err, "dilithium2 signature verification")

		// To be safe.
		if err != nil {
			success = false
		}
	}()

	scheme := schemes.ByName("Dilithium2")
	if scheme == nil {
		return false, errors.New("Dilithium2 scheme not available")
	}

	opts := &sign.SignatureOpts{}
	return scheme.Verify(k.k, data, sig, opts), nil
}

// UnmarshalDilithium2PublicKey returns a public key from input bytes.
func UnmarshalDilithium2PublicKey(data []byte) (PubKey, error) {
	scheme := schemes.ByName("Dilithium2")
	if scheme == nil {
		return nil, errors.New("Dilithium2 scheme not available")
	}

	pub, err := scheme.UnmarshalBinaryPublicKey(data)
	if err != nil {
		return nil, err
	}

	return &Dilithium2PublicKey{
		k: pub,
	}, nil
}

// UnmarshalDilithium2PrivateKey returns a private key from input bytes.
func UnmarshalDilithium2PrivateKey(data []byte) (PrivKey, error) {
	scheme := schemes.ByName("Dilithium2")
	if scheme == nil {
		return nil, errors.New("Dilithium2 scheme not available")
	}

	priv, err := scheme.UnmarshalBinaryPrivateKey(data)
	if err != nil {
		return nil, err
	}

	return &Dilithium2PrivateKey{
		k: priv,
	}, nil
}