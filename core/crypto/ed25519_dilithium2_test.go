package crypto

import (
	"crypto/rand"
	"testing"

	pb "github.com/libp2p/go-libp2p/core/crypto/pb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEd25519Dilithium2KeyGeneration(t *testing.T) {
	priv, pub, err := GenerateEd25519Dilithium2Key(rand.Reader)
	require.NoError(t, err)
	require.NotNil(t, priv)
	require.NotNil(t, pub)

	// Test key types
	assert.Equal(t, pb.KeyType_Ed25519Dilithium2.Number(), priv.Type().Number())
	assert.Equal(t, pb.KeyType_Ed25519Dilithium2.Number(), pub.Type().Number())

	// Test public key extraction
	extractedPub := priv.GetPublic()
	assert.Equal(t, pub.Type(), extractedPub.Type())

	// Test key equality
	assert.True(t, priv.Equals(priv))
	assert.True(t, pub.Equals(pub))
	assert.False(t, priv.Equals(extractedPub))
}

func TestEd25519Dilithium2Signing(t *testing.T) {
	priv, pub, err := GenerateEd25519Dilithium2Key(rand.Reader)
	require.NoError(t, err)

	message := []byte("Hello, Ed25519-Dilithium2 hybrid world!")

	// Sign the message
	signature, err := priv.Sign(message)
	require.NoError(t, err)
	require.NotEmpty(t, signature)

	// Verify the signature
	valid, err := pub.Verify(message, signature)
	require.NoError(t, err)
	assert.True(t, valid)

	// Test with wrong message
	wrongMessage := []byte("Wrong message")
	valid, err = pub.Verify(wrongMessage, signature)
	require.NoError(t, err)
	assert.False(t, valid)
}

func TestEd25519Dilithium2KeyMarshaling(t *testing.T) {
	priv, pub, err := GenerateEd25519Dilithium2Key(rand.Reader)
	require.NoError(t, err)

	// Test private key marshaling
	privBytes, err := priv.Raw()
	require.NoError(t, err)
	require.NotEmpty(t, privBytes)

	// Test public key marshaling
	pubBytes, err := pub.Raw()
	require.NoError(t, err)
	require.NotEmpty(t, pubBytes)

	// Test unmarshaling
	unmarshaledPriv, err := UnmarshalEd25519Dilithium2PrivateKey(privBytes)
	require.NoError(t, err)
	assert.True(t, priv.Equals(unmarshaledPriv))

	unmarshaledPub, err := UnmarshalEd25519Dilithium2PublicKey(pubBytes)
	require.NoError(t, err)
	assert.True(t, pub.Equals(unmarshaledPub))
}
