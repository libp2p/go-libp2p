package memory

import (
	"errors"
	"io"
	"log"
	"net"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

// stream implements network.Stream
type stream struct {
	id int64

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

	sa := newStream(rb, wa, network.DirOutbound)
	sb := newStream(ra, wb, network.DirInbound)

	return sa, sb
}

func newStream(r *io.PipeReader, w *io.PipeWriter, _ network.Direction) *stream {
	s := &stream{
		id:     streamCounter.Add(1),
		read:   r,
		write:  w,
		writeC: make(chan []byte),
		reset:  make(chan struct{}, 1),
		close:  make(chan struct{}, 1),
		closed: make(chan struct{}),
	}
	log.Println("newStream", "id", s.id)

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

func (s *stream) Read(p []byte) (n int, err error) {
	return s.read.Read(p)
}

func (s *stream) CloseWrite() error {
	select {
	case s.close <- struct{}{}:
	default:
	}
	log.Println("waiting close", "id", s.id)
	<-s.closed
	log.Println("closed write", "id", s.id)
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
	s.write.CloseWithError(network.ErrReset)
	s.read.CloseWithError(network.ErrReset)

	select {
	case s.reset <- struct{}{}:
	default:
	}
	<-s.closed

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
	defer close(s.closed)
	defer log.Println("closing write", "id", s.id)

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
				s.writeErr = err
				return
			}
		}
	}
}
