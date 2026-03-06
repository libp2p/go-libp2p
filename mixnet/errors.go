package mixnet

import (
	"errors"
	"fmt"
)

// Error codes for mixnet operations.
const (
	// ErrCodeConfig indicates a configuration-related error.
	ErrCodeConfig = "CONFIG"
	// ErrCodeDiscovery indicates a failure during relay discovery.
	ErrCodeDiscovery = "DISCOVERY"
	// ErrCodeCircuit indicates a failure in circuit establishment or maintenance.
	ErrCodeCircuit = "CIRCUIT"
	// ErrCodeEncryption indicates a failure in the encryption/decryption process.
	ErrCodeEncryption = "ENCRYPTION"
	// ErrCodeCompression indicates a failure during data compression or decompression.
	ErrCodeCompression = "COMPRESSION"
	// ErrCodeSharding indicates a failure in data sharding or reconstruction.
	ErrCodeSharding = "SHARDING"
	// ErrCodeTransport indicates a failure in the underlying network transport.
	ErrCodeTransport = "TRANSPORT"
	// ErrCodeTimeout indicates that an operation timed out.
	ErrCodeTimeout = "TIMEOUT"
	// ErrCodeResource indicates that a resource limit has been reached.
	ErrCodeResource = "RESOURCE"
	// ErrCodeProtocol indicates a protocol-level error or mismatch.
	ErrCodeProtocol = "PROTOCOL"
)

// MixnetError represents an error that occurred during mixnet operations.
// It includes a machine-readable code and an optional underlying cause.
type MixnetError struct {
	Code    string
	Message string
	Cause   error
}

// Error returns the string representation of the MixnetError.
func (e *MixnetError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// Unwrap returns the underlying cause of the error.
func (e *MixnetError) Unwrap() error {
	return e.Cause
}

// WithCause returns a copy of the MixnetError with the specified cause.
func (e *MixnetError) WithCause(err error) *MixnetError {
	return &MixnetError{
		Code:    e.Code,
		Message: e.Message,
		Cause:   err,
	}
}

// ErrConfigInvalid returns a MixnetError for invalid configuration.
func ErrConfigInvalid(msg string) *MixnetError {
	return &MixnetError{Code: ErrCodeConfig, Message: msg}
}

// ErrDiscoveryFailed returns a MixnetError for failed relay discovery.
func ErrDiscoveryFailed(msg string) *MixnetError {
	return &MixnetError{Code: ErrCodeDiscovery, Message: msg}
}

// ErrCircuitFailed returns a MixnetError for failed circuit operations.
func ErrCircuitFailed(msg string) *MixnetError {
	return &MixnetError{Code: ErrCodeCircuit, Message: msg}
}

// ErrEncryptionFailed returns a MixnetError for encryption/decryption failures.
func ErrEncryptionFailed(msg string) *MixnetError {
	return &MixnetError{Code: ErrCodeEncryption, Message: msg}
}

// ErrCompressionFailed returns a MixnetError for compression/decompression failures.
func ErrCompressionFailed(msg string) *MixnetError {
	return &MixnetError{Code: ErrCodeCompression, Message: msg}
}

// ErrShardingFailed returns a MixnetError for sharding/reconstruction failures.
func ErrShardingFailed(msg string) *MixnetError {
	return &MixnetError{Code: ErrCodeSharding, Message: msg}
}

// ErrTransportFailed returns a MixnetError for transport failures.
func ErrTransportFailed(msg string) *MixnetError {
	return &MixnetError{Code: ErrCodeTransport, Message: msg}
}

// ErrTimeout returns a MixnetError for timeout operations.
func ErrTimeout(msg string) *MixnetError {
	return &MixnetError{Code: ErrCodeTimeout, Message: msg}
}

// ErrResourceExhausted returns a MixnetError indicating resource exhaustion.
func ErrResourceExhausted(msg string) *MixnetError {
	return &MixnetError{Code: ErrCodeResource, Message: msg}
}

// ErrProtocolError returns a MixnetError for protocol-level failures.
func ErrProtocolError(msg string) *MixnetError {
	return &MixnetError{Code: ErrCodeProtocol, Message: msg}
}

// ErrReconstructionMissingShards returns a reconstruction error including missing shard IDs.
func ErrReconstructionMissingShards(sessionID string, have, need int, missing []int) *MixnetError {
	return &MixnetError{
		Code:    ErrCodeSharding,
		Message: fmt.Sprintf("reconstruction failed for session %s: have=%d need=%d missing_shard_ids=%v", sessionID, have, need, missing),
	}
}

// IsRetryable returns true if the error indicates a condition that might be resolved by retrying.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}

	var mixnetErr *MixnetError
	if errors.As(err, &mixnetErr) {
		switch mixnetErr.Code {
		case ErrCodeDiscovery, ErrCodeCircuit, ErrCodeTransport, ErrCodeTimeout:
			return true
		}
	}

	// Also check wrapped errors
	return errors.Is(err, contextDeadlineExceeded) || errors.Is(err, contextCanceled)
}

// IsFatal returns true if the error is non-recoverable and should not be retried.
func IsFatal(err error) bool {
	if err == nil {
		return false
	}

	var mixnetErr *MixnetError
	if errors.As(err, &mixnetErr) {
		switch mixnetErr.Code {
		case ErrCodeConfig, ErrCodeEncryption, ErrCodeProtocol:
			return true
		}
	}

	return false
}

// Sentinel errors for common Mixnet error conditions.
var (
	ErrNoCircuitsEstablished = ErrCircuitFailed("no circuits established")
	ErrInsufficientRelays    = ErrDiscoveryFailed("insufficient relays available")
	ErrCircuitClosed         = ErrCircuitFailed("circuit is closed")
	ErrEncryptionNotReady    = ErrEncryptionFailed("encryption not initialized")
	ErrDecryptionFailed      = ErrEncryptionFailed("decryption failed")
	ErrResourceLimit         = ErrResourceExhausted("resource limit exceeded")
	ErrProtocolMismatch      = ErrProtocolError("protocol version mismatch")
)

// Internal sentinel errors for context-related failures.
var (
	contextCanceled         = errors.New("context canceled")
	contextDeadlineExceeded = errors.New("context deadline exceeded")
)
