//go:build js

package libp2pwebtransport

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"syscall/js"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"go.uber.org/multierr"
)

type stream struct {
	conn        *conn
	read, write js.Value
	readMux     sync.Mutex
	readBuf     js.Value
	done        bool

	deadline, readDeadline, writeDeadline atomic.Pointer[time.Time]
}

func newStream(s js.Value, c *conn) *stream {
	return &stream{
		read:    s.Get("readable").Call("getReader"),
		write:   s.Get("writable").Call("getWriter"),
		readBuf: js.Global().Get("Uint8Array").New(0),
		conn:    c,
	}
}

func (s *stream) Read(b []byte) (n int, err error) {
	if len(b) == 0 {
		// We use a zero copy as detection of an empty array bellow, so we need a
		// a non empty destination.
		return
	}

	s.readMux.Lock()
	defer s.readMux.Unlock()
	if s.done {
		return 0, io.EOF
	}
	n = js.CopyBytesToGo(b, s.readBuf)
	if n != 0 {
		s.readBuf = s.readBuf.Call("slice", n)
		return
	}

	ctx := context.Background()
	var deadline, readDeadline time.Time
	if t := s.deadline.Load(); t != nil {
		deadline = *t
	}
	if t := s.readDeadline.Load(); t != nil {
		readDeadline = *t
	}
	switch {
	case deadline.IsZero() && !readDeadline.IsZero():
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, readDeadline)
		defer cancel()
	case !deadline.IsZero():
		max := deadline
		if deadline.Before(readDeadline) {
			max = readDeadline
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, max)
		defer cancel()
	}

	r, err := await(ctx, s.read.Call("read"))
	if err != nil {
		return 0, err
	}
	o := r[0]
	s.readBuf = o.Get("value")
	s.done = o.Get("done").Bool()

	if s.done && s.readBuf.IsUndefined() {
		return 0, io.EOF
	}

	n = js.CopyBytesToGo(b, s.readBuf)
	s.readBuf = s.readBuf.Call("slice", n)
	return
}

func (s *stream) Write(b []byte) (n int, err error) {
	if len(b) == 0 {
		return
	}

	ctx := context.Background()
	var deadline, writeDeadline time.Time
	if t := s.deadline.Load(); t != nil {
		deadline = *t
	}
	if t := s.writeDeadline.Load(); t != nil {
		writeDeadline = *t
	}
	switch {
	case deadline.IsZero() && !writeDeadline.IsZero():
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, writeDeadline)
		defer cancel()
	case !deadline.IsZero():
		max := deadline
		if deadline.Before(writeDeadline) {
			max = writeDeadline
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, max)
		defer cancel()
	}

	_, err = await(ctx, s.write.Call("write", byteSliceToJS(b)))
	if err != nil {
		return 0, err
	}

	return len(b), nil
}

func (s *stream) Reset() error {
	return multierr.Combine(s.resetWrite(), s.CloseRead())
}

func (s *stream) ResetWithError(_ network.StreamErrorCode) error {
	return s.Reset()
}

func (s *stream) Close() error {
	return multierr.Combine(s.CloseWrite(), s.CloseRead())
}

func (s *stream) CloseWrite() error {
	_, err := await(context.Background(), s.write.Call("close"))
	return err
}

func (s *stream) resetWrite() error {
	_, err := await(context.Background(), s.write.Call("abort"))
	return err
}

func (s *stream) CloseRead() error {
	_, err := await(context.Background(), s.read.Call("cancel"))
	return err
}

func (s *stream) RemoteAddr() net.Addr { return s.conn.RemoteAddr() }
func (s *stream) LocalAddr() net.Addr  { return s.conn.LocalAddr() }

func (s *stream) SetDeadline(t time.Time) error {
	s.deadline.Store(&t)
	return nil
}

func (s *stream) SetReadDeadline(t time.Time) error {
	s.readDeadline.Store(&t)
	return nil
}

func (s *stream) SetWriteDeadline(t time.Time) error {
	s.writeDeadline.Store(&t)
	return nil
}
