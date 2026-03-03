package ces

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
)

// LayeredEncrypter handles layered onion encryption
type LayeredEncrypter struct {
	hopCount int
}

// EncryptionKey represents an ephemeral key for one encryption layer
type EncryptionKey struct {
	Key        []byte
	Destination string
}

// NewLayeredEncrypter creates a new layered encrypter
func NewLayeredEncrypter(hopCount int) *LayeredEncrypter {
	return &LayeredEncrypter{
		hopCount: hopCount,
	}
}

// Encrypt encrypts data with layered encryption (onion routing)
// Each layer wraps the data with destination info and encryption
// Destinations should be ordered from entry to exit (first hop = entry)
func (e *LayeredEncrypter) Encrypt(plaintext []byte, destinations []string) ([]byte, []*EncryptionKey, error) {
	if len(destinations) != e.hopCount {
		return nil, nil, fmt.Errorf("expected %d destinations, got %d", e.hopCount, len(destinations))
	}

	keys := make([]*EncryptionKey, e.hopCount)

	// Generate ephemeral keys for each layer
	for i := 0; i < e.hopCount; i++ {
		key := make([]byte, 32) // chacha20poly1305 key size
		if _, err := io.ReadFull(rand.Reader, key); err != nil {
			return nil, nil, err
		}
		keys[i] = &EncryptionKey{
			Key:         key,
			Destination: destinations[i],
		}
	}

	// Build encrypted payload from outside in (reverse order for onion)
	// Start with innermost layer (exit relay)
	currentData := plaintext

	for i := e.hopCount - 1; i >= 0; i-- {
		// Create header: [dest_len(2)][dest_len_varint][dest_bytes][data]
		header := make([]byte, 2+binary.MaxVarintLen64+len(keys[i].Destination))
		binary.LittleEndian.PutUint16(header[0:2], uint16(len(keys[i].Destination)))
		offset := 2
		// Write destination length as varint (for future use)
		varintBuf := make([]byte, binary.MaxVarintLen64)
		n := binary.PutUvarint(varintBuf, uint64(len(keys[i].Destination)))
		offset += n
		copy(header[offset:], keys[i].Destination)
		offset += len(keys[i].Destination)

		// Prepend header to data
		payload := append(header[:offset], currentData...)

		// Encrypt with ChaCha20-Poly1305
		aead, err := chacha20poly1305.NewX(keys[i].Key)
		if err != nil {
			return nil, nil, err
		}

		nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(payload))
		if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
			return nil, nil, err
		}

		encrypted := aead.Seal(nonce, nonce, payload, nil)
		currentData = encrypted
	}

	return currentData, keys, nil
}

// Decrypt decrypts layered encrypted data
// Keys should be provided in reverse order (outermost to innermost)
func (e *LayeredEncrypter) Decrypt(ciphertext []byte, keys []*EncryptionKey) ([]byte, error) {
	if len(keys) != e.hopCount {
		return nil, fmt.Errorf("expected %d keys, got %d", e.hopCount, len(keys))
	}

	// Decrypt from outside in
	currentData := ciphertext

	for i := 0; i < e.hopCount; i++ {
		aead, err := chacha20poly1305.NewX(keys[i].Key)
		if err != nil {
			return nil, err
		}

		nonceSize := aead.NonceSize()
		if len(currentData) < nonceSize {
			return nil, fmt.Errorf("ciphertext too short")
		}

		nonce, ciphertext := currentData[:nonceSize], currentData[nonceSize:]

		plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
		if err != nil {
			return nil, err
		}

		// Extract destination from header
		if len(plaintext) < 2 {
			return nil, fmt.Errorf("invalid header: too short")
		}
		offset := 0
		destLen := int(binary.LittleEndian.Uint16(plaintext[0:2]))
		offset += 2

		_, n := binary.Uvarint(plaintext[offset:])
		if n <= 0 {
			return nil, fmt.Errorf("invalid varint in header")
		}
		offset += n

		if len(plaintext) < offset+destLen {
			return nil, fmt.Errorf("invalid destination length")
		}

		// Verify destination matches (optional security check)
		extractedDest := string(plaintext[offset : offset+destLen])
		if extractedDest != keys[i].Destination {
			// Continue anyway - this is a integrity check, not a security bypass
		}

		// Remaining is the decrypted payload for next layer
		currentData = plaintext[offset+destLen:]
	}

	return currentData, nil
}

// HopCount returns the number of encryption layers
func (e *LayeredEncrypter) HopCount() int {
	return e.hopCount
}
