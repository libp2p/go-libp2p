package crypto_test

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	crypto_pb "github.com/libp2p/go-libp2p/core/crypto/pb"
)

var message = []byte("Libp2p is the _best_!")

type testCase struct {
	keyType          crypto_pb.KeyType
	gen              func(i io.Reader) (crypto.PrivKey, crypto.PubKey, error)
	sigDeterministic bool
}

var keyTypes = []testCase{
	{
		keyType: crypto_pb.KeyType_ECDSA,
		gen:     crypto.GenerateECDSAKeyPair,
	},
	{
		keyType:          crypto_pb.KeyType_Secp256k1,
		sigDeterministic: true,
		gen:              crypto.GenerateSecp256k1Key,
	},
	{
		keyType:          crypto_pb.KeyType_RSA,
		sigDeterministic: true,
		gen: func(i io.Reader) (crypto.PrivKey, crypto.PubKey, error) {
			return crypto.GenerateRSAKeyPair(2048, i)
		},
	},
}

func fname(kt crypto_pb.KeyType, ext string) string {
	return fmt.Sprintf("test_data/%d.%s", kt, ext)
}

func TestFixtures(t *testing.T) {
	for _, tc := range keyTypes {
		t.Run(tc.keyType.String(), func(t *testing.T) {
			pubBytes, err := os.ReadFile(fname(tc.keyType, "pub"))
			if err != nil {
				t.Fatal(err)
			}
			privBytes, err := os.ReadFile(fname(tc.keyType, "priv"))
			if err != nil {
				t.Fatal(err)
			}
			sigBytes, err := os.ReadFile(fname(tc.keyType, "sig"))
			if err != nil {
				t.Fatal(err)
			}
			pub, err := crypto.UnmarshalPublicKey(pubBytes)
			if err != nil {
				t.Fatal(err)
			}
			pubBytes2, err := crypto.MarshalPublicKey(pub)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(pubBytes2, pubBytes) {
				t.Fatal("encoding round-trip failed")
			}
			priv, err := crypto.UnmarshalPrivateKey(privBytes)
			if err != nil {
				t.Fatal(err)
			}
			privBytes2, err := crypto.MarshalPrivateKey(priv)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(privBytes2, privBytes) {
				t.Fatal("encoding round-trip failed")
			}
			ok, err := pub.Verify(message, sigBytes)
			if !ok || err != nil {
				t.Fatal("failed to validate signature with public key")
			}

			if tc.sigDeterministic {
				sigBytes2, err := priv.Sign(message)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(sigBytes2, sigBytes) {
					t.Fatal("signature not deterministic")
				}
			}
		})
	}
}

func init() {
	// set to true to re-generate test data
	if false {
		generate()
		panic("generated")
	}
}

// generate re-generates test data
func generate() {
	for _, tc := range keyTypes {
		priv, pub, err := tc.gen(rand.Reader)
		if err != nil {
			panic(err)
		}
		pubb, err := crypto.MarshalPublicKey(pub)
		if err != nil {
			panic(err)
		}
		privb, err := crypto.MarshalPrivateKey(priv)
		if err != nil {
			panic(err)
		}
		sig, err := priv.Sign(message)
		if err != nil {
			panic(err)
		}
		os.WriteFile(fname(tc.keyType, "pub"), pubb, 0o666)
		os.WriteFile(fname(tc.keyType, "priv"), privb, 0o666)
		os.WriteFile(fname(tc.keyType, "sig"), sig, 0o666)
	}
}
