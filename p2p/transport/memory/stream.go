package memory

import (
	"errors"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

type stream struct {
	inC  <-chan []byte
	outC chan<- []byte

	readCloseC  chan struct{}
	writeCloseC chan struct{}

	mu     sync.Mutex
	closed bool

	deadline      time.Time
	readDeadline  time.Time
	writeDeadline time.Time
}

func newStream() *stream {
	return &stream{
		inC:         make(<-chan []byte),
		outC:        make(chan<- []byte),
		readCloseC:  make(chan struct{}),
		writeCloseC: make(chan struct{}),
	}
}

func (s *stream) Read(b []byte) (n int, err error) {
	if s.closed {
		return 0, network.ErrReset
	}

	select {
	case <-s.readCloseC:
		err = network.ErrReset
	case r, ok := <-s.inC:
		if !ok {
			err = network.ErrReset
		} else {
			n = copy(b, r)
		}
	}

	return n, err
}

func (s *stream) Write(b []byte) (n int, err error) {
	select {
	case <-s.writeCloseC:
		err = network.ErrReset
	case s.outC <- b:
		n = len(b)
	default:
		err = network.ErrReset
	}

	return n, err
}

func (s *stream) Reset() error {
	s.CloseWrite()
	s.CloseRead()
	return nil
}

func (s *stream) Close() error {
	s.CloseRead()

	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()

	return nil
}

func (s *stream) CloseRead() error {
	select {
	case s.readCloseC <- struct{}{}:
	default:
		return errors.New("failed to close stream read")
	}

	return nil
}

func (s *stream) CloseWrite() error {
	select {
	case s.writeCloseC <- struct{}{}:
	default:
		return errors.New("failed to close stream write")
	}

	return nil
}

func (s *stream) SetDeadline(deadline time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.deadline = deadline
	return nil
}

func (s *stream) SetReadDeadline(readDeadline time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.readDeadline = readDeadline
	return nil
}
func (s *stream) SetWriteDeadline(writeDeadline time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.writeDeadline = writeDeadline
	return nil
}
