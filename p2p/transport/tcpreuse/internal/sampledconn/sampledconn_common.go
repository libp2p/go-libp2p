package sampledconn

import (
	"errors"
	"io"
	"net"
	"syscall"
	"time"

	manet "github.com/multiformats/go-multiaddr/net"
)

const peekSize = 3

type PeekedBytes = [peekSize]byte

var errNotSupported = errors.New("not supported on this platform")

var ErrNotTCPConn = errors.New("passed conn is not a TCPConn")

func PeekBytes(conn manet.Conn) (PeekedBytes, manet.Conn, error) {
	if c, ok := conn.(syscall.Conn); ok {
		b, err := OSPeekConn(c)
		if err == nil {
			return b, conn, nil
		}
		if err != errNotSupported {
			return PeekedBytes{}, nil, err
		}
		// Fallback to wrapping the coonn
	}

	if c, ok := conn.(ManetTCPConnInterface); ok {
		return newFallbackSampledConn(c)
	}

	return PeekedBytes{}, nil, ErrNotTCPConn
}

type fallbackPeekingConn struct {
	ManetTCPConnInterface
	peekedBytes PeekedBytes
	bytesPeeked uint8
}

// tcpConnInterface is the interface for TCPConn's functions
// NOTE: `SyscallConn() (syscall.RawConn, error)` is here to make using this as
// a TCP Conn easier, but it's a potential footgun as you could skipped the
// peeked bytes if using the fallback
type tcpConnInterface interface {
	net.Conn
	syscall.Conn

	CloseRead() error
	CloseWrite() error

	SetLinger(sec int) error
	SetKeepAlive(keepalive bool) error
	SetKeepAlivePeriod(d time.Duration) error
	SetNoDelay(noDelay bool) error
	MultipathTCP() (bool, error)

	io.ReaderFrom
	io.WriterTo
}

type ManetTCPConnInterface interface {
	manet.Conn
	tcpConnInterface
}

func newFallbackSampledConn(conn ManetTCPConnInterface) (PeekedBytes, *fallbackPeekingConn, error) {
	s := &fallbackPeekingConn{ManetTCPConnInterface: conn}
	_, err := io.ReadFull(conn, s.peekedBytes[:])
	if err != nil {
		return s.peekedBytes, nil, err
	}
	return s.peekedBytes, s, nil
}

func (sc *fallbackPeekingConn) Read(b []byte) (int, error) {
	if int(sc.bytesPeeked) != len(sc.peekedBytes) {
		red := copy(b, sc.peekedBytes[sc.bytesPeeked:])
		sc.bytesPeeked += uint8(red)
		return red, nil
	}

	return sc.ManetTCPConnInterface.Read(b)
}
