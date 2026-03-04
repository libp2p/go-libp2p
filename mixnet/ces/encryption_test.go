package ces

import (
	"bytes"
	"fmt"
	"testing"
)

func TestEncryption_LayeredEncryptDecrypt(t *testing.T) {
	// Test with 3 hops = 3 encryption layers
	encrypter := NewLayeredEncrypter(3)

	plaintext := []byte("Secret message for layered encryption test!")

	// Encrypt with all layers
	encrypted, keys, err := encrypter.Encrypt(plaintext, []string{"dest1", "dest2", "dest3"})
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if len(keys) != 3 {
		t.Errorf("expected 3 keys, got %d", len(keys))
	}

	// Decrypt in reverse order
	decrypted, err := encrypter.Decrypt(encrypted, keys)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}

	if !bytes.Equal(plaintext, decrypted) {
		t.Errorf("roundtrip failed:\n  got:      %q\n  expected: %q", decrypted, plaintext)
	}
}

func TestEncryption_SingleHop(t *testing.T) {
	encrypter := NewLayeredEncrypter(1)

	plaintext := []byte("Single hop message")
	encrypted, keys, err := encrypter.Encrypt(plaintext, []string{"dest1"})
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	decrypted, err := encrypter.Decrypt(encrypted, keys)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}

	if !bytes.Equal(plaintext, decrypted) {
		t.Errorf("roundtrip failed")
	}
}

func TestEncryption_DifferentKeysPerSession(t *testing.T) {
	encrypter1 := NewLayeredEncrypter(2)
	encrypter2 := NewLayeredEncrypter(2)

	plaintext := []byte("Same message")

	// Encrypt with first encrypter
	encrypted1, keys1, _ := encrypter1.Encrypt(plaintext, []string{"a", "b"})

	// Encrypt same message with second encrypter - should produce different ciphertext
	encrypted2, keys2, _ := encrypter2.Encrypt(plaintext, []string{"a", "b"})

	// Ciphertexts should be different due to ephemeral keys
	if bytes.Equal(encrypted1, encrypted2) {
		t.Error("expected different ciphertexts for different sessions")
	}

	// But both should decrypt to same plaintext
	decrypted1, _ := encrypter1.Decrypt(encrypted1, keys1)
	decrypted2, _ := encrypter2.Decrypt(encrypted2, keys2)

	if !bytes.Equal(decrypted1, decrypted2) {
		t.Error("both should decrypt to original plaintext")
	}
}

func TestEncryption_HeaderIncluded(t *testing.T) {
	encrypter := NewLayeredEncrypter(2)

	plaintext := []byte("Message with header")
	destinations := []string{"/ip4/1.2.3.4/tcp/1234", "/ip4/5.6.7.8/tcp/5678"}

	encrypted, keys, err := encrypter.Encrypt(plaintext, destinations)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	// Encrypted data should include routing headers (longer than plaintext)
	if len(encrypted) <= len(plaintext) {
		t.Errorf("encrypted data should include headers, got %d <= %d", len(encrypted), len(plaintext))
	}

	// Keys should contain destinations
	for i, key := range keys {
		if key.Destination != destinations[i] {
			t.Errorf("key destination mismatch: got %s, want %s", key.Destination, destinations[i])
		}
	}

	_ = encrypted
}

func TestEncryption_MismatchedKeys(t *testing.T) {
	encrypter := NewLayeredEncrypter(2)

	plaintext := []byte("Test")
	encrypted, _, err := encrypter.Encrypt(plaintext, []string{"a", "b"})
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	// Try to decrypt with wrong number of keys
	_, err = encrypter.Decrypt(encrypted, []*EncryptionKey{{Key: []byte("short")}})
	if err == nil {
		t.Error("expected error with mismatched keys")
	}
}

func TestEncryption_TenHops(t *testing.T) {
	encrypter := NewLayeredEncrypter(10)

	plaintext := []byte("Ten hop encryption test message!")

	destinations := make([]string, 10)
	for i := 0; i < 10; i++ {
		destinations[i] = fmt.Sprintf("/ip4/192.168.1.%d/tcp/400%d", i+1, i+1)
	}

	encrypted, keys, err := encrypter.Encrypt(plaintext, destinations)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	if len(keys) != 10 {
		t.Errorf("expected 10 keys, got %d", len(keys))
	}

	decrypted, err := encrypter.Decrypt(encrypted, keys)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}

	if !bytes.Equal(plaintext, decrypted) {
		t.Error("ten hop roundtrip failed")
	}
}

func TestEncryption_EmptyData(t *testing.T) {
	encrypter := NewLayeredEncrypter(2)

	encrypted, keys, err := encrypter.Encrypt([]byte{}, []string{"a", "b"})
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	decrypted, err := encrypter.Decrypt(encrypted, keys)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}

	if len(decrypted) != 0 {
		t.Errorf("expected empty decrypted data, got %d bytes", len(decrypted))
	}
}

func TestLayeredEncrypter_HopCount(t *testing.T) {
	encrypter := NewLayeredEncrypter(5)
	if encrypter.HopCount() != 5 {
		t.Errorf("expected hop count 5, got %d", encrypter.HopCount())
	}
}
