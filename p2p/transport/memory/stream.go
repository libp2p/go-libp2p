package memory

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

// stream implements network.Stream
type stream struct {
	id   int64
	conn *conn

	wrMu sync.Mutex    // Serialize Write operations
	buf  *bytes.Buffer // Buffer for partial reads

	// Used by local Read to interact with remote Write.
	rdRx <-chan []byte

	// Used by local Write to interact with remote Read.
	wrTx chan<- []byte

	once       sync.Once // Protects closing localDone
	localDone  chan struct{}
	remoteDone <-chan struct{}

	reset       chan struct{}
	close       chan struct{}
	readClosed  atomic.Bool
	writeClosed atomic.Bool
}

var ErrClosed = errors.New("stream closed")

func newStreamPair() (*stream, *stream) {
	cb1 := make(chan []byte, 1)
	cb2 := make(chan []byte, 1)

	done1 := make(chan struct{})
	done2 := make(chan struct{})

	sa := newStream(cb1, cb2, done1, done2)
	sb := newStream(cb2, cb1, done2, done1)

	return sa, sb
}

func newStream(rdRx <-chan []byte, wrTx chan<- []byte, localDone chan struct{}, remoteDone <-chan struct{}) *stream {
	s := &stream{
		rdRx:       rdRx,
		wrTx:       wrTx,
		buf:        new(bytes.Buffer),
		localDone:  localDone,
		remoteDone: remoteDone,
		reset:      make(chan struct{}, 1),
		close:      make(chan struct{}, 1),
	}

	return s
}

func (p *stream) Write(b []byte) (int, error) {
	if p.writeClosed.Load() {
		return 0, ErrClosed
	}

	n, err := p.write(b)
	if err != nil && err != io.ErrClosedPipe {
		err = &net.OpError{Op: "write", Net: "pipe", Err: err}
	}
	return n, err
}

func (p *stream) write(b []byte) (n int, err error) {
	switch {
	case isClosedChan(p.localDone):
		return 0, io.ErrClosedPipe
	case isClosedChan(p.remoteDone):
		return 0, io.ErrClosedPipe
	}

	p.wrMu.Lock() // Ensure entirety of b is written together
	defer p.wrMu.Unlock()

	select {
	case <-p.close:
		return n, ErrClosed
	case <-p.reset:
		return n, network.ErrReset
	case p.wrTx <- b:
		n += len(b)
	case <-p.localDone:
		return n, io.ErrClosedPipe
	case <-p.remoteDone:
		return n, io.ErrClosedPipe
	}

	return n, nil
}

func (p *stream) Read(b []byte) (int, error) {
	if p.readClosed.Load() {
		return 0, ErrClosed
	}

	n, err := p.read(b)
	if err != nil && err != io.EOF && err != io.ErrClosedPipe {
		err = &net.OpError{Op: "read", Net: "pipe", Err: err}
	}

	return n, err
}

func (p *stream) read(b []byte) (n int, err error) {
	switch {
	case isClosedChan(p.localDone):
		return 0, io.ErrClosedPipe
	case isClosedChan(p.remoteDone):
		return 0, io.EOF
	}

	select {
	case <-p.reset:
		return n, network.ErrReset
	case bw, ok := <-p.rdRx:
		if !ok {
			p.readClosed.Store(true)
			return 0, io.EOF
		}

		p.buf.Write(bw)
	case <-p.localDone:
		return 0, io.ErrClosedPipe
	case <-p.remoteDone:
		return 0, io.EOF
	default:
		n, err = p.buf.Read(b)
	}

	return n, err
}

func (s *stream) CloseWrite() error {
	select {
	case s.close <- struct{}{}:
	default:
	}

	s.writeClosed.Store(true)
	return nil
}

func (s *stream) CloseRead() error {
	s.readClosed.Store(true)
	return nil
}

func (s *stream) Close() error {
	_ = s.CloseRead()
	return s.CloseWrite()
}

func (s *stream) Reset() error {
	select {
	case s.reset <- struct{}{}:
	default:
	}

	s.once.Do(func() {
		close(s.localDone)
	})

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

func isClosedChan(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}
