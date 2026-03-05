package mixnet

import (
	"crypto/rand"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	sessionKeySize   = 32
	sessionNonceSize = 24
)

type sessionKey struct {
	Key   []byte
	Nonce []byte
}

func encryptSessionPayload(plaintext []byte) ([]byte, []byte, error) {
	key := make([]byte, sessionKeySize)
	nonce := make([]byte, sessionNonceSize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, nil, err
	}
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, nil, err
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, nil)
	return ciphertext, encodeSessionKeyData(sessionKey{Key: key, Nonce: nonce}), nil
}

func decryptSessionPayload(ciphertext []byte, key sessionKey) ([]byte, error) {
	if len(key.Key) != sessionKeySize || len(key.Nonce) != sessionNonceSize {
		return nil, fmt.Errorf("invalid session key material")
	}
	aead, err := chacha20poly1305.NewX(key.Key)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, key.Nonce, ciphertext, nil)
}

func encodeSessionKeyData(key sessionKey) []byte {
	buf := make([]byte, 0, sessionNonceSize+sessionKeySize)
	buf = append(buf, key.Nonce...)
	buf = append(buf, key.Key...)
	return buf
}

func decodeSessionKeyData(data []byte) (sessionKey, error) {
	if len(data) != sessionNonceSize+sessionKeySize {
		return sessionKey{}, fmt.Errorf("invalid key data length")
	}
	nonce := make([]byte, sessionNonceSize)
	key := make([]byte, sessionKeySize)
	copy(nonce, data[:sessionNonceSize])
	copy(key, data[sessionNonceSize:])
	return sessionKey{Key: key, Nonce: nonce}, nil
}
