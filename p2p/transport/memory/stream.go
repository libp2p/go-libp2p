package memory

import (
	"errors"
	"io"
	"net"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

// stream implements network.Stream
type stream struct {
	id   int64
	conn *conn

	read   *io.PipeReader
	write  *io.PipeWriter
	writeC chan []byte

	reset  chan struct{}
	close  chan struct{}
	closed chan struct{}

	writeErr error
}

var ErrClosed = errors.New("stream closed")

func newStreamPair() (*stream, *stream) {
	ra, wb := io.Pipe()
	rb, wa := io.Pipe()

	sa := newStream(wa, ra, network.DirOutbound)
	sb := newStream(wb, rb, network.DirInbound)

	return sa, sb
}

func newStream(w *io.PipeWriter, r *io.PipeReader, _ network.Direction) *stream {
	s := &stream{
		id:     streamCounter.Add(1),
		read:   r,
		write:  w,
		writeC: make(chan []byte),
		reset:  make(chan struct{}, 1),
		close:  make(chan struct{}, 1),
		closed: make(chan struct{}),
	}

	go s.writeLoop()
	return s
}

func (s *stream) Write(p []byte) (int, error) {
	cpy := make([]byte, len(p))
	copy(cpy, p)

	select {
	case <-s.closed:
		return 0, s.writeErr
	case s.writeC <- cpy:
	}

	return len(p), nil
}

func (s *stream) Read(p []byte) (int, error) {
	return s.read.Read(p)
}

func (s *stream) CloseWrite() error {
	select {
	case s.close <- struct{}{}:
	default:
	}
	<-s.closed
	if !errors.Is(s.writeErr, ErrClosed) {
		return s.writeErr
	}
	return nil

}

func (s *stream) CloseRead() error {
	return s.read.CloseWithError(ErrClosed)
}

func (s *stream) Close() error {
	_ = s.CloseRead()
	return s.CloseWrite()
}

func (s *stream) Reset() error {
	// Cancel any pending reads/writes with an error.
	s.write.CloseWithError(network.ErrReset)
	s.read.CloseWithError(network.ErrReset)

	select {
	case s.reset <- struct{}{}:
	default:
	}
	<-s.closed
	// No meaningful error case here.
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

func (s *stream) writeLoop() {
	defer s.teardown()

	for {
		// Reset takes precedent.
		select {
		case <-s.reset:
			s.writeErr = network.ErrReset
			return
		default:
		}

		select {
		case <-s.reset:
			s.writeErr = network.ErrReset
			return
		case <-s.close:
			s.writeErr = s.write.Close()
			if s.writeErr == nil {
				s.writeErr = ErrClosed
			}
			return
		case p := <-s.writeC:
			if _, err := s.write.Write(p); err != nil {
				s.cancelWrite(err)
				return
			}
		}
	}
}

func (s *stream) cancelWrite(err error) {
	s.write.CloseWithError(err)
	s.writeErr = err
}

func (s *stream) teardown() {
	// at this point, no streams are writing.
	if s.conn != nil {
		s.conn.removeStream(s.id)
	}

	// Mark as closed.
	close(s.closed)
}
