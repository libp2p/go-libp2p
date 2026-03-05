package mixnet

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// MixStream provides a read/write stream interface over Mixnet.
type MixStream struct {
	mixnet    *Mixnet
	dest      peer.ID
	sessionID string
	ch        <-chan []byte
	mu        sync.Mutex
	closed    bool
	readBuf   []byte
}

// OpenStream creates a Mixnet stream to the destination.
func (m *Mixnet) OpenStream(ctx context.Context, dest peer.ID) (*MixStream, error) {
	if _, err := m.EstablishConnection(ctx, dest); err != nil {
		return nil, err
	}
	sessionID := fmt.Sprintf("%s-%d", dest.String(), time.Now().UnixNano())
	ch := m.destHandler.registerSession(sessionID)
	return &MixStream{
		mixnet:    m,
		dest:      dest,
		sessionID: sessionID,
		ch:        ch,
	}, nil
}

func (s *MixStream) Read(p []byte) (int, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, io.EOF
	}
	if len(s.readBuf) > 0 {
		n := copy(p, s.readBuf)
		s.readBuf = s.readBuf[n:]
		s.mu.Unlock()
		return n, nil
	}
	s.mu.Unlock()

	data, ok := <-s.ch
	if !ok {
		return 0, io.EOF
	}
	if len(data) <= len(p) {
		copy(p, data)
		return len(data), nil
	}
	n := copy(p, data[:len(p)])
	s.mu.Lock()
	s.readBuf = append(s.readBuf, data[n:]...)
	s.mu.Unlock()
	return n, nil
}

func (s *MixStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	if s.dest == "" {
		s.mu.Unlock()
		return 0, fmt.Errorf("cannot write to inbound-only stream")
	}
	s.mu.Unlock()

	if err := s.mixnet.SendWithSession(context.Background(), s.dest, p, s.sessionID); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *MixStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	s.mixnet.destHandler.unregisterSession(s.sessionID)
	return nil
}

// AcceptStream waits for an inbound mixnet session and returns a MixStream for it.
func (m *Mixnet) AcceptStream(ctx context.Context) (*MixStream, error) {
	if m.destHandler == nil {
		return nil, fmt.Errorf("no destination handler")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case sessionID := <-m.destHandler.inboundCh:
		ch := m.destHandler.registerSession(sessionID)
		return &MixStream{
			mixnet:    m,
			dest:      "",
			sessionID: sessionID,
			ch:        ch,
		}, nil
	}
}
