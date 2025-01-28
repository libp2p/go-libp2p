package main

import (
	"os"

	"github.com/libp2p/go-libp2p/core/crypto"
)

// LoadIdentity reads a private key from the given path and, if it does not
// exist, generates a new one.
func LoadIdentity(keyPath string) (crypto.PrivKey, error) {
	if _, err := os.Stat(keyPath); err == nil {
		return ReadIdentity(keyPath)
	} else if os.IsNotExist(err) {
		logger.Infof("Generating peer identity in %s\n", keyPath)
		return GenerateIdentity(keyPath)
	} else {
		return nil, err
	}
}

// ReadIdentity reads a private key from the given path.
func ReadIdentity(path string) (crypto.PrivKey, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	return crypto.UnmarshalPrivateKey(bytes)
}

// GenerateIdentity writes a new random private key to the given path.
func GenerateIdentity(path string) (crypto.PrivKey, error) {
	privk, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 0)
	if err != nil {
		return nil, err
	}

	bytes, err := crypto.MarshalPrivateKey(privk)
	if err != nil {
		return nil, err
	}

	err = os.WriteFile(path, bytes, 0400)

	return privk, err
}
