package memory

import (
	"errors"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

type stream struct {
	id int32

	inC  chan []byte
	outC chan []byte

	readCloseC  chan struct{}
	writeCloseC chan struct{}

	closed atomic.Bool
}

func newStream(id int32, in, out chan []byte) *stream {
	return &stream{
		id:          id,
		inC:         in,
		outC:        out,
		readCloseC:  make(chan struct{}),
		writeCloseC: make(chan struct{}),
	}
}

func (s *stream) Read(b []byte) (n int, err error) {
	if s.closed.Load() {
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
	if s.closed.Load() {
		return 0, network.ErrReset
	}

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
	if err := s.CloseWrite(); err != nil {
		return err
	}
	if err := s.CloseRead(); err != nil {
		return err
	}
	return nil
}

func (s *stream) Close() error {
	if err := s.CloseRead(); err != nil {
		return err
	}

	s.closed.Store(true)
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

func (s *stream) SetDeadline(_ time.Time) error {
	return nil
}

func (s *stream) SetReadDeadline(_ time.Time) error {
	return nil
}
func (s *stream) SetWriteDeadline(_ time.Time) error {
	return nil
}
