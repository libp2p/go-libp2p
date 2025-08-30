package crypto

import (
	"crypto/rand"
	"testing"

	pb "github.com/libp2p/go-libp2p/core/crypto/pb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMLDSA65KeyGeneration(t *testing.T) {
	priv, pub, err := GenerateMLDSA65Key(rand.Reader)
	require.NoError(t, err)
	require.NotNil(t, priv)
	require.NotNil(t, pub)

	// Test key types
	assert.Equal(t, pb.KeyType_MLDSA65.Number(), priv.Type().Number())
	assert.Equal(t, pb.KeyType_MLDSA65.Number(), pub.Type().Number())

	// Test public key extraction
	extractedPub := priv.GetPublic()
	assert.Equal(t, pub.Type(), extractedPub.Type())

	// Test key equality
	assert.True(t, priv.Equals(priv))
	assert.True(t, pub.Equals(pub))
	assert.False(t, priv.Equals(extractedPub))
}

func TestMLDSA65Signing(t *testing.T) {
	priv, pub, err := GenerateMLDSA65Key(rand.Reader)
	require.NoError(t, err)

	message := []byte("Hello, ML-DSA-65 world!")

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

func TestMLDSA65KeyMarshaling(t *testing.T) {
	priv, pub, err := GenerateMLDSA65Key(rand.Reader)
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
	unmarshaledPriv, err := UnmarshalMLDSA65PrivateKey(privBytes)
	require.NoError(t, err)
	assert.True(t, priv.Equals(unmarshaledPriv))

	unmarshaledPub, err := UnmarshalMLDSA65PublicKey(pubBytes)
	require.NoError(t, err)
	assert.True(t, pub.Equals(unmarshaledPub))
}
