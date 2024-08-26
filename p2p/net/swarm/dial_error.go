package swarm

import (
	"fmt"
	"os"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"

	ma "github.com/multiformats/go-multiaddr"
)

// maxDialDialErrors is the maximum number of dial errors we record
const maxDialDialErrors = 16

// DialError is the error type returned when dialing.
type DialError struct {
	Peer       peer.ID
	DialErrors []TransportError
	Cause      error
	Skipped    int
}

func (e *DialError) Timeout() bool {
	return os.IsTimeout(e.Cause)
}

func (e *DialError) recordErr(addr ma.Multiaddr, err error) {
	if len(e.DialErrors) >= maxDialDialErrors {
		e.Skipped++
		return
	}
	e.DialErrors = append(e.DialErrors, TransportError{Address: addr, Cause: err})
}

func (e *DialError) Error() string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "failed to dial %s:", e.Peer)
	if e.Cause != nil {
		fmt.Fprintf(&builder, " %s", e.Cause)
	}
	for _, te := range e.DialErrors {
		fmt.Fprintf(&builder, "\n  * [%s] %s", te.Address, te.Cause)
	}
	if e.Skipped > 0 {
		fmt.Fprintf(&builder, "\n    ... skipping %d errors ...", e.Skipped)
	}
	return builder.String()
}

func (e *DialError) Unwrap() []error {
	if e == nil {
		return nil
	}

	errs := make([]error, 0, len(e.DialErrors)+1)
	if e.Cause != nil {
		errs = append(errs, e.Cause)
	}
	for i := 0; i < len(e.DialErrors); i++ {
		errs = append(errs, &e.DialErrors[i])
	}
	return errs
}

var _ error = (*DialError)(nil)

// TransportError is the error returned when dialing a specific address.
type TransportError struct {
	Address ma.Multiaddr
	Cause   error
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("failed to dial %s: %s", e.Address, e.Cause)
}

func (e *TransportError) Unwrap() error {
	return e.Cause
}

var _ error = (*TransportError)(nil)
