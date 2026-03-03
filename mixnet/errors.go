package mixnet

import (
	"errors"
	"fmt"
)

// Error codes for mixnet operations (Req 19)
const (
	ErrCodeConfig       = "CONFIG"
	ErrCodeDiscovery    = "DISCOVERY"
	ErrCodeCircuit      = "CIRCUIT"
	ErrCodeEncryption   = "ENCRYPTION"
	ErrCodeCompression  = "COMPRESSION"
	ErrCodeSharding    = "SHARDING"
	ErrCodeTransport    = "TRANSPORT"
	ErrCodeTimeout      = "TIMEOUT"
	ErrCodeResource     = "RESOURCE"
	ErrCodeProtocol     = "PROTOCOL"
)

// MixnetError represents an error in mixnet operations (Req 19)
type MixnetError struct {
	Code    string
	Message string
	Cause   error
}

// Error implements the error interface
func (e *MixnetError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// Unwrap returns the underlying cause
func (e *MixnetError) Unwrap() error {
	return e.Cause
}

// WithCause wraps an error with context
func (e *MixnetError) WithCause(err error) *MixnetError {
	return &MixnetError{
		Code:    e.Code,
		Message: e.Message,
		Cause:   err,
	}
}

// Common error constructors (Req 19.1)

// ErrConfigInvalid returns a configuration error
func ErrConfigInvalid(msg string) *MixnetError {
	return &MixnetError{Code: ErrCodeConfig, Message: msg}
}

// ErrDiscoveryFailed returns a discovery error
func ErrDiscoveryFailed(msg string) *MixnetError {
	return &MixnetError{Code: ErrCodeDiscovery, Message: msg}
}

// ErrCircuitFailed returns a circuit error
func ErrCircuitFailed(msg string) *MixnetError {
	return &MixnetError{Code: ErrCodeCircuit, Message: msg}
}

// ErrEncryptionFailed returns an encryption error
func ErrEncryptionFailed(msg string) *MixnetError {
	return &MixnetError{Code: ErrCodeEncryption, Message: msg}
}

// ErrCompressionFailed returns a compression error
func ErrCompressionFailed(msg string) *MixnetError {
	return &MixnetError{Code: ErrCodeCompression, Message: msg}
}

// ErrShardingFailed returns a sharding error
func ErrShardingFailed(msg string) *MixnetError {
	return &MixnetError{Code: ErrCodeSharding, Message: msg}
}

// ErrTransportFailed returns a transport error
func ErrTransportFailed(msg string) *MixnetError {
	return &MixnetError{Code: ErrCodeTransport, Message: msg}
}

// ErrTimeout returns a timeout error
func ErrTimeout(msg string) *MixnetError {
	return &MixnetError{Code: ErrCodeTimeout, Message: msg}
}

// ErrResourceExhausted returns a resource limit error (Req 20)
func ErrResourceExhausted(msg string) *MixnetError {
	return &MixnetError{Code: ErrCodeResource, Message: msg}
}

// ErrProtocolError returns a protocol error
func ErrProtocolError(msg string) *MixnetError {
	return &MixnetError{Code: ErrCodeProtocol, Message: msg}
}

// IsRetryable returns true if the error indicates a retryable condition (Req 19.2)
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

// IsFatal returns true if the error is fatal and should not be retried (Req 19.2)
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

// Sentinel errors for common cases
var (
	ErrNoCircuitsEstablished = ErrCircuitFailed("no circuits established")
	ErrInsufficientRelays    = ErrDiscoveryFailed("insufficient relays available")
	ErrCircuitClosed         = ErrCircuitFailed("circuit is closed")
	ErrEncryptionNotReady    = ErrEncryptionFailed("encryption not initialized")
	ErrDecryptionFailed     = ErrEncryptionFailed("decryption failed")
	ErrResourceLimit        = ErrResourceExhausted("resource limit exceeded")
	ErrProtocolMismatch     = ErrProtocolError("protocol version mismatch")
)

// Context for error wrapping
var (
	contextCanceled       = errors.New("context canceled")
	contextDeadlineExceeded = errors.New("context deadline exceeded")
)
