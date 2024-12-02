//go:build !unix

package sampledconn

import (
	"syscall"
)

func OSPeekConn(conn syscall.Conn) (PeekedBytes, error) {
	return PeekedBytes{}, errNotSupported
}
