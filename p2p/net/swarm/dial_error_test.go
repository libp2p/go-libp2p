package swarm

import (
	"net"
	"os"
	"testing"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

func TestTransportError(t *testing.T) {
	aa := ma.StringCast("/ip4/1.2.3.4/tcp/1234")
	te := &TransportError{Address: aa, Cause: ErrDialBackoff}
	require.ErrorIs(t, te, ErrDialBackoff, "TransportError should implement Unwrap")
}

func TestDialError(t *testing.T) {
	de := &DialError{Peer: "pid", Cause: ErrGaterDisallowedConnection}
	require.ErrorIs(t, de, ErrGaterDisallowedConnection,
		"DialError Unwrap should handle DialError.Cause")
	require.ErrorIs(t, de, de, "DialError Unwrap should handle match to self")

	aa := ma.StringCast("/ip4/1.2.3.4/tcp/1234")
	ab := ma.StringCast("/ip6/1::1/udp/1234/quic-v1")
	de = &DialError{
		Peer: "pid",
		DialErrors: []TransportError{
			{Address: aa, Cause: ErrDialBackoff}, {Address: ab, Cause: ErrNoTransport},
		},
	}
	require.ErrorIs(t, de, ErrDialBackoff, "DialError.Unwrap should traverse TransportErrors")
	require.ErrorIs(t, de, ErrNoTransport, "DialError.Unwrap should traverse TransportErrors")

	de = &DialError{
		Peer: "pid",
		DialErrors: []TransportError{{Address: ab, Cause: ErrNoTransport},
			// wrapped error 2 levels deep
			{Address: aa, Cause: &net.OpError{
				Op:  "write",
				Net: "tcp",
				Err: &os.SyscallError{
					Syscall: "connect",
					Err:     os.ErrPermission,
				},
			}},
		},
	}
	require.ErrorIs(t, de, os.ErrPermission, "DialError.Unwrap should traverse TransportErrors")

}
