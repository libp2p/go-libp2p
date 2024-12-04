//go:build windows

package sampledconn

import (
	"errors"
	"io"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const fionbio = 0x8004667e

// updateBlocking updates the blocking mode of the file descriptor.
// It returns true if the blocking mode was changed, and false if it was already in the desired mode.
// If an error occurs, it returns the error.
func updateBlocking(fd windows.Handle, blocking bool) (bool, error) {
	// Determine the desired mode
	var desiredMode uint32
	if !blocking {
		desiredMode = 1
	} else {
		desiredMode = 0
	}

	// Query the current mode
	var currentMode uint32
	err := windows.WSAIoctl(fd, fionbio, (*byte)(unsafe.Pointer(&currentMode)), 4, nil, 0, nil, nil, 0)
	if err != nil {
		return false, err
	}

	if currentMode == desiredMode {
		return false, nil
	}

	// Update to the desired mode
	err = windows.WSAIoctl(fd, fionbio, (*byte)(unsafe.Pointer(&desiredMode)), 4, nil, 0, nil, nil, 0)
	if err != nil {
		return false, err
	}

	return true, nil
}

func OSPeekConn(conn syscall.Conn) (PeekedBytes, error) {
	s := PeekedBytes{}

	rawConn, err := conn.SyscallConn()
	if err != nil {
		return s, err
	}

	var updatedBlocking bool
	ctlErr := rawConn.Control(func(fd uintptr) {
		updatedBlocking, err = updateBlocking(windows.Handle(fd), true)
	})
	if ctlErr != nil {
		return s, ctlErr
	}
	if err != nil {
		return s, err
	}
	if updatedBlocking {
		defer func() {
			_ = rawConn.Control(func(fd uintptr) {
				_, _ = updateBlocking(windows.Handle(fd), false)
			})
		}()
	}

	var readErr error
	var n uint32
	err = rawConn.Read(func(fd uintptr) bool {
		flags := uint32(windows.MSG_PEEK | windows.MSG_WAITALL)
		wsabuf := windows.WSABuf{
			Len: uint32(len(s)),
			Buf: &s[0],
		}

		readErr = windows.WSARecv(windows.Handle(fd), &wsabuf, 1, &n, &flags, nil, nil)
		return !errors.Is(readErr, windows.WSAEWOULDBLOCK)
	})
	if err != nil {
		return s, err
	}
	if readErr != nil {
		return s, readErr
	}
	if n < peekSize {
		return s, io.EOF
	}

	return s, nil
}
