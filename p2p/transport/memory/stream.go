package memory

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

// onceError is an object that will only store an error once.
type onceError struct {
	sync.Mutex // guards following
	err        error
}

func (a *onceError) Store(err error) {
	a.Lock()
	defer a.Unlock()
	if a.err != nil {
		return
	}
	a.err = err
}
func (a *onceError) Load() error {
	a.Lock()
	defer a.Unlock()
	return a.err
}

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

	closeOnce  sync.Once // Protects closing localDone
	localDone  chan struct{}
	remoteDone <-chan struct{}

	resetOnce   sync.Once // Protects closing localReset
	localReset  chan struct{}
	remoteReset <-chan struct{}

	rerr onceError
	werr onceError
}

var ErrClosed = errors.New("stream closed")

func newStreamPair() (*stream, *stream) {
	cb1 := make(chan []byte, 1)
	cb2 := make(chan []byte, 1)

	done1 := make(chan struct{})
	done2 := make(chan struct{})

	reset1 := make(chan struct{})
	reset2 := make(chan struct{})

	sa := newStream(cb1, cb2, done1, done2, reset1, reset2)
	sb := newStream(cb2, cb1, done2, done1, reset2, reset1)

	return sa, sb
}

func newStream(rdRx <-chan []byte, wrTx chan<- []byte, localDone chan struct{}, remoteDone <-chan struct{}, localReset chan struct{}, remoteReset <-chan struct{}) *stream {
	s := &stream{
		id:          streamCounter.Add(1),
		rdRx:        rdRx,
		wrTx:        wrTx,
		buf:         new(bytes.Buffer),
		localDone:   localDone,
		remoteDone:  remoteDone,
		localReset:  localReset,
		remoteReset: remoteReset,
	}

	return s
}

func (p *stream) Write(b []byte) (int, error) {
	if err := p.werr.Load(); err != nil {
		return 0, err
	}

	return p.write(b)
	//if err != nil && err != io.ErrClosedPipe && err != network.ErrReset {
	//	err = &net.OpError{Op: "write", Net: "pipe", Err: err}
	//}
	//
	//return n, err
}

func (p *stream) write(b []byte) (n int, err error) {
	switch {
	case isClosedChan(p.remoteReset):
		return 0, network.ErrReset
	case isClosedChan(p.remoteDone):
		return 0, io.ErrClosedPipe
	}

	p.wrMu.Lock() // Ensure entirety of b is written together
	defer p.wrMu.Unlock()

	select {
	case p.wrTx <- b:
		n += len(b)
	}

	return n, nil
}

func (p *stream) Read(b []byte) (int, error) {
	if err := p.rerr.Load(); err != nil {
		return 0, err
	}

	return p.read(b)
	//if err != nil && err != io.EOF && err != io.ErrClosedPipe && err != network.ErrReset {
	//	err = &net.OpError{Op: "read", Net: "pipe", Err: err}
	//}
	//
	//return n, err
}

func (p *stream) read(b []byte) (n int, err error) {
	var readErr error

	switch {
	case isClosedChan(p.remoteReset):
		err = network.ErrReset
	case isClosedChan(p.remoteDone):
		err = io.EOF
	}

	select {
	case bw, ok := <-p.rdRx:
		if !ok {
			err = io.EOF
			p.rerr.Store(err)
			return
		}

		p.buf.Write(bw)
	default:
	}

	n, readErr = p.buf.Read(b)
	if err == nil {
		err = readErr
	}

	return n, err
}

func (s *stream) CloseWrite() error {
	s.werr.Store(ErrClosed)
	return nil
}

func (s *stream) CloseRead() error {
	s.rerr.Store(ErrClosed)
	return nil
}

func (s *stream) Close() error {
	s.closeOnce.Do(func() {
		close(s.localDone)
	})

	_ = s.CloseRead()
	return s.CloseWrite()
}

func (s *stream) Reset() error {
	s.rerr.Store(network.ErrReset)
	s.werr.Store(network.ErrReset)

	s.resetOnce.Do(func() {
		close(s.localReset)
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
