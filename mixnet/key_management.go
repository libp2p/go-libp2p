// Package mixnet provides cryptographic key management and secure erasure.
package mixnet

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"runtime"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"

	"github.com/libp2p/go-libp2p/mixnet/ces"
)

// ============================================================
// Req 16: Cryptographic Key Management
// ============================================================

// KeyManager manages cryptographic keys for mixnet circuits.
// Each circuit has its own set of keys, properly associated with circuit IDs.
type KeyManager struct {
	mu           sync.RWMutex
	circuitKeys  map[string]*CircuitKeys // circuitID -> keys
	host         host.Host
	sessionKeys  map[string]*SessionKeys // sessionID -> keys
}

// CircuitKeys holds keys for a specific circuit.
type CircuitKeys struct {
	CircuitID  string
	EntryKey   []byte   // Key for entry relay
	HopKeys    [][]byte // Keys for each hop
	CreatedAt  time.Time
	Expiry     time.Time
}

// SessionKeys holds keys for a specific session.
type SessionKeys struct {
	SessionID      string
	EncryptionKeys []*ces.EncryptionKey
	CreatedAt      time.Time
}

// NewKeyManager creates a new KeyManager instance.
func NewKeyManager(h host.Host) *KeyManager {
	return &KeyManager{
		host:        h,
		circuitKeys: make(map[string]*CircuitKeys),
		sessionKeys: make(map[string]*SessionKeys),
	}
}

// GenerateCircuitKeys generates keys for a specific circuit.
// This implements Req 16.1 - Keys associated with circuit IDs.
func (km *KeyManager) GenerateCircuitKeys(circuitID string, hopCount int) (*CircuitKeys, error) {
	km.mu.Lock()
	defer km.mu.Unlock()

	keys := &CircuitKeys{
		CircuitID: circuitID,
		HopKeys:   make([][]byte, hopCount),
		CreatedAt: time.Now(),
		Expiry:    time.Now().Add(1 * time.Hour), // Keys expire after 1 hour
	}

	// Generate random keys for each hop
	for i := 0; i < hopCount; i++ {
		key := make([]byte, 32) // ChaCha20 key size
		if _, err := io.ReadFull(rand.Reader, key); err != nil {
			return nil, fmt.Errorf("failed to generate key: %w", err)
		}
		keys.HopKeys[i] = key
	}

	// Store keys associated with circuit ID
	km.circuitKeys[circuitID] = keys

	return keys, nil
}

// GetCircuitKeys retrieves keys for a specific circuit.
func (km *KeyManager) GetCircuitKeys(circuitID string) (*CircuitKeys, bool) {
	km.mu.RLock()
	defer km.mu.RUnlock()

	keys, ok := km.circuitKeys[circuitID]
	return keys, ok
}

// StoreSessionKeys stores encryption keys for a specific session.
// This implements Req 16.4 - Key exchange mechanism.
func (km *KeyManager) StoreSessionKeys(sessionID string, keys []*ces.EncryptionKey) {
	km.mu.Lock()
	defer km.mu.Unlock()

	km.sessionKeys[sessionID] = &SessionKeys{
		SessionID:      sessionID,
		EncryptionKeys: keys,
		CreatedAt:      time.Now(),
	}
}

// GetSessionKeys retrieves encryption keys for a specific session.
func (km *KeyManager) GetSessionKeys(sessionID string) (*SessionKeys, bool) {
	km.mu.RLock()
	defer km.mu.RUnlock()

	keys, ok := km.sessionKeys[sessionID]
	return keys, ok
}

// SecureErase securely erases all keys from memory.
// This implements Req 16.3 - Proper secure erase.
func (km *KeyManager) SecureErase() {
	km.mu.Lock()
	defer km.mu.Unlock()

	// Erase circuit keys
	for circuitID, keys := range km.circuitKeys {
		for i := range keys.HopKeys {
			secureEraseBytes(keys.HopKeys[i])
			keys.HopKeys[i] = nil
		}
		if keys.EntryKey != nil {
			secureEraseBytes(keys.EntryKey)
			keys.EntryKey = nil
		}
		delete(km.circuitKeys, circuitID)
	}

	// Erase session keys
	for sessionID, keys := range km.sessionKeys {
		for _, key := range keys.EncryptionKeys {
			if key != nil && key.Key != nil {
				secureEraseBytes(key.Key)
				key.Key = nil
			}
		}
		delete(km.sessionKeys, sessionID)
	}
}

// EraseCircuitKeys removes and securely erases keys for a specific circuit.
func (km *KeyManager) EraseCircuitKeys(circuitID string) {
	km.mu.Lock()
	defer km.mu.Unlock()

	if keys, ok := km.circuitKeys[circuitID]; ok {
		for i := range keys.HopKeys {
			secureEraseBytes(keys.HopKeys[i])
			keys.HopKeys[i] = nil
		}
		delete(km.circuitKeys, circuitID)
	}
}

// secureEraseBytes overwrites memory with zeros to prevent key recovery.
func secureEraseBytes(b []byte) {
	if b == nil {
		return
	}
	for i := range b {
		b[i] = 0
	}
	runtime.KeepAlive(b)
}

// ============================================================
// Req 18: Graceful Shutdown
// ============================================================

// GracefulCloser provides graceful shutdown with in-flight data completion.
type GracefulCloser struct {
	mu           sync.RWMutex
	closing      bool
	inFlight     int64
	drainTimeout time.Duration
	stopCh       chan struct{}
}

// NewGracefulCloser creates a new graceful closer.
func NewGracefulCloser(drainTimeout time.Duration) *GracefulCloser {
	return &GracefulCloser{
		drainTimeout: drainTimeout,
		stopCh:       make(chan struct{}),
	}
}

// BeginClose initiates graceful shutdown, waiting for in-flight data.
func (gc *GracefulCloser) BeginClose() error {
	gc.mu.Lock()
	if gc.closing {
		gc.mu.Unlock()
		return fmt.Errorf("already closing")
	}
	gc.closing = true
	gc.mu.Unlock()

	// Wait for in-flight data to complete with timeout
	ctx, cancel := context.WithTimeout(context.Background(), gc.drainTimeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		gc.mu.RLock()
		inFlight := gc.inFlight
		gc.mu.RUnlock()

		if inFlight == 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("drain timeout after %v with %d in-flight", gc.drainTimeout, inFlight)
		case <-ticker.C:
			continue
		}
	}
}

// IncrementInFlight increments the in-flight counter.
func (gc *GracefulCloser) IncrementInFlight() {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	gc.inFlight++
}

// DecrementInFlight decrements the in-flight counter.
func (gc *GracefulCloser) DecrementInFlight() {
	gc.mu.Lock()
	defer gc.mu.Unlock()
	gc.inFlight--
}

// IsClosing returns true if graceful close is in progress.
func (gc *GracefulCloser) IsClosing() bool {
	gc.mu.RLock()
	defer gc.mu.RUnlock()
	return gc.closing
}

// StopChan returns the stop channel.
func (gc *GracefulCloser) StopChan() <-chan struct{} {
	return gc.stopCh
}

// ============================================================
// Req 19: Error Handling with Retry Logic
// ============================================================

// RetryableError indicates an error that can be retried.
type RetryableError struct {
	Err    error
	Reason string
}

func (e *RetryableError) Error() string {
	return fmt.Sprintf("%s: %v", e.Reason, e.Err)
}

// RetryConfig holds configuration for retry behavior.
type RetryConfig struct {
	MaxRetries      int
	InitialDelay    time.Duration
	MaxDelay        time.Duration
	BackoffMultiplier float64
}

// DefaultRetryConfig returns sensible retry defaults.
func DefaultRetryConfig() *RetryConfig {
	return &RetryConfig{
		MaxRetries:        3,
		InitialDelay:      100 * time.Millisecond,
		MaxDelay:          5 * time.Second,
		BackoffMultiplier: 2.0,
	}
}

// RetryWithBackoff retries an operation with exponential backoff.
func RetryWithBackoff(ctx context.Context, cfg *RetryConfig, op func() error) error {
	if cfg == nil {
		cfg = DefaultRetryConfig()
	}

	var lastErr error
	delay := cfg.InitialDelay

	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		err := op()
		if err == nil {
			return nil
		}

		lastErr = err

		// Check if error is retryable using existing IsRetryable function
		if !IsRetryable(err) {
			return err
		}

		// Check if we've exceeded retries
		if attempt >= cfg.MaxRetries {
			break
		}

		// Wait with backoff
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}

		// Exponential backoff
		delay = time.Duration(float64(delay) * cfg.BackoffMultiplier)
		if delay > cfg.MaxDelay {
			delay = cfg.MaxDelay
		}
	}

	return fmt.Errorf("max retries exceeded, last error: %w", lastErr)
}

// MixnetWithKeyManagement extends Mixnet with proper key management.
type MixnetWithKeyManagement struct {
	*Mixnet
	keyManager *KeyManager
	closer     *GracefulCloser
}

// NewMixnetWithKeyManagement creates a mixnet with proper key management.
func NewMixnetWithKeyManagement(cfg *MixnetConfig, h host.Host, r routing.Routing) (*MixnetWithKeyManagement, error) {
	mix, err := NewMixnet(cfg, h, r)
	if err != nil {
		return nil, err
	}

	return &MixnetWithKeyManagement{
		Mixnet:     mix,
		keyManager: NewKeyManager(h),
		closer:     NewGracefulCloser(10 * time.Second),
	}, nil
}

// KeyManager returns the key manager.
func (m *MixnetWithKeyManagement) KeyManager() *KeyManager {
	return m.keyManager
}

// CloseWithGrace shuts down gracefully, waiting for in-flight data.
func (m *MixnetWithKeyManagement) CloseWithGrace() error {
	// Signal we're closing
	m.closer.BeginClose()

	// Erase all keys securely
	m.keyManager.SecureErase()

	// Do regular close
	return m.Mixnet.Close()
}

// SendWithRetry sends data with automatic retry on transient failures.
func (m *MixnetWithKeyManagement) SendWithRetry(ctx context.Context, dest peer.ID, data []byte) error {
	cfg := DefaultRetryConfig()
	return RetryWithBackoff(ctx, cfg, func() error {
		return m.Mixnet.Send(ctx, dest, data)
	})
}
