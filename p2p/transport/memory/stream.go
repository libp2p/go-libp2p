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
	wa, wb := rb, ra

	in := newStream(rb, wb, network.DirInbound)
	out := newStream(ra, wa, network.DirOutbound)
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

// How to handle errors with writes?
func (s *stream) Write(p []byte) (n int, err error) {
	if s.closed.Load() {
		return 0, ErrClosed
	}

	for i := 0; i < len(p); i++ {
		select {
		case <-s.reset:
			err = network.ErrReset
			return
		case <-s.closeWrite:
			err = ErrClosed
			return
		case s.write <- p[i]:
			n = i
		default:
			err = io.ErrClosedPipe
		}
	}

	return n + 1, err
}

func (s *stream) Read(p []byte) (n int, err error) {
	if s.closed.Load() {
		return 0, ErrClosed
	}

	for n = 0; n < len(p); n++ {
		select {
		case <-s.reset:
			err = network.ErrReset
			return
		case <-s.closeRead:
			err = ErrClosed
			return
		case b, ok := <-s.read:
			if !ok {
				err = io.EOF
				return
			}
			p[n] = b
		default:
			err = io.EOF
			return
		}
	}

	return
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
