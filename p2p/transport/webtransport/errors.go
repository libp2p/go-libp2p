package libp2pwebtransport

import (
	"fmt"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/quic-go/webtransport-go"
)

// StreamError represents a WebTransport stream error that wraps both
// the underlying WebTransport error and network.ErrReset
type StreamError struct {
	underlying *webtransport.StreamError
	msg        string
}

func (e *StreamError) Error() string {
	if e.msg != "" {
		return fmt.Sprintf("%s: %v", e.msg, e.underlying)
	}
	return e.underlying.Error()
}

// Unwrap implements the errors.Unwrap interface to return multiple errors
func (e *StreamError) Unwrap() []error {
	return []error{network.ErrReset, e.underlying}
}

// Is implements the errors interface to allow errors.Is checks
func (e *StreamError) Is(target error) bool {
	return target == network.ErrReset
}

func newStreamError(err *webtransport.StreamError, msg string) *StreamError {
	return &StreamError{
		underlying: err,
		msg:        msg,
	}
}

func parseStreamError(err error) error {
	if err == nil {
		return nil
	}

	if streamErr, ok := err.(*webtransport.StreamError); ok {
		return &network.StreamError{
			ErrorCode:      network.StreamErrorCode(streamErr.ErrorCode),
			Remote:         false, // WebTransport doesn't provide remote info
			TransportError: streamErr,
		}
	}
	return err
}
