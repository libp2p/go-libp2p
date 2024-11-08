package memory

import (
	"io"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

type stream struct {
	id int32

	r      *io.PipeReader
	w      *io.PipeWriter
	writeC chan []byte

	readCloseC  chan struct{}
	writeCloseC chan struct{}

	closed atomic.Bool
}

func newStream(id int32, r *io.PipeReader, w *io.PipeWriter) *stream {
	s := &stream{
		id:          id,
		r:           r,
		w:           w,
		writeC:      make(chan []byte, 1),
		readCloseC:  make(chan struct{}, 1),
		writeCloseC: make(chan struct{}, 1),
	}

	go func() {
		for {
			select {
			case b := <-s.writeC:
				if _, err := w.Write(b); err != nil {
					return
				}
			case <-s.writeCloseC:
				return
			}
		}
	}()

	return s
}

func (s *stream) Read(b []byte) (int, error) {
	return s.r.Read(b)
}

func (s *stream) Write(b []byte) (int, error) {
	if s.closed.Load() {
		return 0, network.ErrReset
	}

	select {
	case <-s.writeCloseC:
		return 0, network.ErrReset
	case s.writeC <- b:
		return len(b), nil
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
	s.CloseRead()
	s.CloseWrite()
	return nil
}

func (s *stream) CloseRead() error {
	return s.r.CloseWithError(network.ErrReset)
}

func (s *stream) CloseWrite() error {
	select {
	case s.writeCloseC <- struct{}{}:
	default:
	}

	s.closed.Store(true)
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
