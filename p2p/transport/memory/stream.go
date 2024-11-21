package memory

import (
	"errors"
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

// stream implements network.Stream
type stream struct {
	id int64

	write chan byte
	read  chan byte

	reset      chan struct{}
	closeRead  chan struct{}
	closeWrite chan struct{}
	closed     atomic.Bool
}

var ErrClosed = errors.New("stream closed")

func newStreamPair() (*stream, *stream) {
	ra, rb := make(chan byte, 4096), make(chan byte, 4096)

	in := newStream(rb, ra, network.DirInbound)
	out := newStream(ra, rb, network.DirOutbound)
	return in, out
}

func newStream(r, w chan byte, _ network.Direction) *stream {
	s := &stream{
		id:         streamCounter.Add(1),
		read:       r,
		write:      w,
		reset:      make(chan struct{}, 1),
		closeRead:  make(chan struct{}, 1),
		closeWrite: make(chan struct{}, 1),
	}

	return s
}

func (s *stream) Write(p []byte) (n int, err error) {
	if s.closed.Load() {
		return 0, ErrClosed
	}

	select {
	case <-s.reset:
		return 0, network.ErrReset
	case <-s.closeWrite:
		return 0, ErrClosed
	default:
	}

	for n < len(p) {
		select {
		case <-s.closeWrite:
			return n, ErrClosed
		case <-s.reset:
			return n, network.ErrReset
		case s.write <- p[n]:
			n++
		default:
			err = io.ErrClosedPipe
			return
		}
	}

	return
}

func (s *stream) Read(p []byte) (n int, err error) {
	if s.closed.Load() {
		return 0, ErrClosed
	}

	select {
	case <-s.reset:
		return 0, network.ErrReset
	case <-s.closeRead:
		return 0, ErrClosed
	default:
	}

	for n < len(p) {
		select {
		case <-s.closeRead:
			return n, ErrClosed
		case <-s.reset:
			return n, network.ErrReset
		case b, ok := <-s.read:
			if !ok {
				return n, ErrClosed
			}
			p[n] = b
			n++
		default:
			return n, io.EOF
		}
	}

	return n, nil
}

func (s *stream) CloseWrite() error {
	s.closeWrite <- struct{}{}
	return nil
}

func (s *stream) CloseRead() error {
	s.closeRead <- struct{}{}
	return nil
}

func (s *stream) Close() error {
	s.closed.Store(true)
	return nil
}

func (s *stream) Reset() error {
	s.reset <- struct{}{}
	return nil
}

func (s *stream) SetDeadline(t time.Time) error {
	return &net.OpError{Op: "set", Net: "pipe", Source: nil, Addr: nil, Err: errors.New("deadline not supported")}
}

func (s *stream) SetReadDeadline(t time.Time) error {
	return &net.OpError{Op: "set", Net: "pipe", Source: nil, Addr: nil, Err: errors.New("deadline not supported")}
}

func (s *stream) SetWriteDeadline(t time.Time) error {
	return &net.OpError{Op: "set", Net: "pipe", Source: nil, Addr: nil, Err: errors.New("deadline not supported")}
}
