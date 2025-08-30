package crypto

import (
	"crypto/rand"
	"testing"

	pb "github.com/libp2p/go-libp2p/core/crypto/pb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestDilithium2KeyGeneration(t *testing.T) {
	priv, pub, err := GenerateDilithium2Key(rand.Reader)
	require.NoError(t, err)
	require.NotNil(t, priv)
	require.NotNil(t, pub)

	// Test key types
	assert.Equal(t, pb.KeyType_Dilithium2.Number(), priv.Type().Number())
	assert.Equal(t, pb.KeyType_Dilithium2.Number(), pub.Type().Number())

	// Test public key extraction
	extractedPub := priv.GetPublic()
	assert.Equal(t, pub.Type(), extractedPub.Type())

	// Test key equality
	assert.True(t, priv.Equals(priv))
	assert.True(t, pub.Equals(pub))
	assert.False(t, priv.Equals(extractedPub))
}

func TestDilithium2Signing(t *testing.T) {
	priv, pub, err := GenerateDilithium2Key(rand.Reader)
	require.NoError(t, err)

	message := []byte("Hello, post-quantum world!")

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

func TestDilithium2KeyMarshaling(t *testing.T) {
	priv, pub, err := GenerateDilithium2Key(rand.Reader)
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
	unmarshaledPriv, err := UnmarshalDilithium2PrivateKey(privBytes)
	require.NoError(t, err)
	assert.True(t, priv.Equals(unmarshaledPriv))

	unmarshaledPub, err := UnmarshalDilithium2PublicKey(pubBytes)
	require.NoError(t, err)
	assert.True(t, pub.Equals(unmarshaledPub))
}

func TestDilithium2MarshalLoop(t *testing.T) {
	priv, pub, err := GenerateDilithium2Key(rand.Reader)
	require.NoError(t, err)

	t.Run("PrivateKey", func(t *testing.T) {
		for name, f := range map[string]func() ([]byte, error){
			"Marshal": func() ([]byte, error) {
				return MarshalPrivateKey(priv)
			},
		} {
			t.Run(name, func(t *testing.T) {
				bts, err := f()
				if err != nil {
					t.Fatal(err)
				}

				privNew, err := UnmarshalPrivateKey(bts)
				if err != nil {
					t.Fatal(err)
				}

				if !priv.Equals(privNew) || !privNew.Equals(priv) {
					t.Fatal("keys are not equal")
				}

				msg := []byte("My child, my sister,\nThink of the rapture\nOf living together there!")
				signed, err := privNew.Sign(msg)
				if err != nil {
					t.Fatal(err)
				}

				ok, err := privNew.GetPublic().Verify(msg, signed)
				if err != nil {
					t.Fatal(err)
				}

				if !ok {
					t.Fatal("signature didn't match")
				}
			})
		}
	})

	t.Run("PublicKey", func(t *testing.T) {
		for name, f := range map[string]func() ([]byte, error){
			"Marshal": func() ([]byte, error) {
				return MarshalPublicKey(pub)
			},
		} {
			t.Run(name, func(t *testing.T) {
				bts, err := f()
				if err != nil {
					t.Fatal(err)
				}
				pubNew, err := UnmarshalPublicKey(bts)
				if err != nil {
					t.Fatal(err)
				}

				if !pub.Equals(pubNew) || !pubNew.Equals(pub) {
					t.Fatal("keys are not equal")
				}
			})
		}
	})
}

func TestDilithium2SignZero(t *testing.T) {
	priv, pub, err := GenerateDilithium2Key(rand.Reader)
	require.NoError(t, err)

	data := make([]byte, 0)
	sig, err := priv.Sign(data)
	require.NoError(t, err)
	require.NotEmpty(t, sig)

	ok, err := pub.Verify(data, sig)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestDilithium2KeyEqual(t *testing.T) {
	priv1, pub1, err := GenerateDilithium2Key(rand.Reader)
	require.NoError(t, err)

	priv2, pub2, err := GenerateDilithium2Key(rand.Reader)
	require.NoError(t, err)

	// Test KeyEqual function
	assert.True(t, KeyEqual(priv1, priv1))
	assert.True(t, KeyEqual(pub1, pub1))
	assert.True(t, KeyEqual(priv2, priv2))
	assert.True(t, KeyEqual(pub2, pub2))

	// Different keys should not be equal
	assert.False(t, KeyEqual(priv1, priv2))
	assert.False(t, KeyEqual(pub1, pub2))
	assert.False(t, KeyEqual(priv1, pub1))
}

func TestDilithium2UnmarshalErrors(t *testing.T) {
	t.Run("PublicKey", func(t *testing.T) {
		t.Run("Invalid data length", func(t *testing.T) {
			data, err := proto.Marshal(&pb.PublicKey{
				Type: pb.KeyType_Dilithium2.Enum(),
				Data: []byte{42},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := UnmarshalPublicKey(data); err == nil {
				t.Fatal("expected an error")
			}
		})
	})

	t.Run("PrivateKey", func(t *testing.T) {
		t.Run("Invalid data length", func(t *testing.T) {
			data, err := proto.Marshal(&pb.PrivateKey{
				Type: pb.KeyType_Dilithium2.Enum(),
				Data: []byte{42},
			})
			if err != nil {
				t.Fatal(err)
			}

			_, err = UnmarshalPrivateKey(data)
			if err == nil {
				t.Fatal("expected an error")
			}
		})
	})
}
