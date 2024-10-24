package memory

import (
	"errors"
	"io"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

type stream struct {
	id int32

	r *io.PipeReader
	w *io.PipeWriter

	readCloseC  chan struct{}
	writeCloseC chan struct{}

	closed atomic.Bool
}

func newStream(id int32, r *io.PipeReader, w *io.PipeWriter) *stream {
	return &stream{
		id:          id,
		r:           r,
		w:           w,
		readCloseC:  make(chan struct{}, 1),
		writeCloseC: make(chan struct{}, 1),
	}
}

func (s *stream) Read(b []byte) (int, error) {
	if s.closed.Load() {
		return 0, network.ErrReset
	}

	select {
	case <-s.readCloseC:
		return 0, network.ErrReset
	default:
		return s.r.Read(b)
	}
}

func (s *stream) Write(b []byte) (int, error) {
	if s.closed.Load() {
		return 0, network.ErrReset
	}

	select {
	case <-s.writeCloseC:
		return 0, network.ErrReset
	default:
		return s.w.Write(b)
	}
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
