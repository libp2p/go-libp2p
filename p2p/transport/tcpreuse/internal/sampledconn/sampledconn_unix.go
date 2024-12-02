//go:build unix

package sampledconn

import (
	"errors"
	"syscall"
)

func OSPeekConn(conn syscall.Conn) (PeekedBytes, error) {
	s := PeekedBytes{}

	rawConn, err := conn.SyscallConn()
	if err != nil {
		return s, err
	}

	readBytes := 0
	var readErr error
	err = rawConn.Read(func(fd uintptr) bool {
		for readBytes < peekSize {
			var n int
			n, _, readErr = syscall.Recvfrom(int(fd), s[readBytes:], syscall.MSG_PEEK)
			if errors.Is(readErr, syscall.EAGAIN) {
				return false
			}
			if readErr != nil {
				return true
			}
			readBytes += n
		}
		return true
	})
	if readErr != nil {
		return s, readErr
	}
	if err != nil {
		return s, err
	}

	return s, nil
}
