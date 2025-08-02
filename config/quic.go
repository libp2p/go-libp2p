//go:build !js
// +build !js

package config

import (
	"crypto/sha256"
	"io"

	"golang.org/x/crypto/hkdf"

	"github.com/libp2p/go-libp2p/core/crypto"

	"github.com/quic-go/quic-go"
)

const (
	statelessResetKeyInfo = "libp2p quic stateless reset key"
	tokenGeneratorKeyInfo = "libp2p quic token generator key"
)

func PrivKeyToStatelessResetKey(key crypto.PrivKey) (quic.StatelessResetKey, error) {
	var statelessResetKey quic.StatelessResetKey
	keyBytes, err := key.Raw()
	if err != nil {
		return statelessResetKey, err
	}
	keyReader := hkdf.New(sha256.New, keyBytes, nil, []byte(statelessResetKeyInfo))
	if _, err := io.ReadFull(keyReader, statelessResetKey[:]); err != nil {
		return statelessResetKey, err
	}
	return statelessResetKey, nil
}

func PrivKeyToTokenGeneratorKey(key crypto.PrivKey) (quic.TokenGeneratorKey, error) {
	var tokenKey quic.TokenGeneratorKey
	keyBytes, err := key.Raw()
	if err != nil {
		return tokenKey, err
	}
	keyReader := hkdf.New(sha256.New, keyBytes, nil, []byte(tokenGeneratorKeyInfo))
	if _, err := io.ReadFull(keyReader, tokenKey[:]); err != nil {
		return tokenKey, err
	}
	return tokenKey, nil
}
