package memory

import (
	"context"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
	"net"
	"sync"
	"sync/atomic"
)

type listener struct {
	ctx    context.Context
	cancel context.CancelFunc
	laddr  ma.Multiaddr

	mu          sync.Mutex
	connID      atomic.Int32
	streamCh    chan *stream
	connections map[int32]*conn
}

func (l *listener) Multiaddr() ma.Multiaddr {
	return l.laddr
}

func newListener(laddr ma.Multiaddr, streamCh chan *stream) tpt.Listener {
	ctx, cancel := context.WithCancel(context.Background())
	return &listener{
		ctx:      ctx,
		cancel:   cancel,
		laddr:    laddr,
		streamCh: streamCh,
	}
}

// Accept accepts new connections.
func (l *listener) Accept() (tpt.CapableConn, error) {
	select {
	case s := <-l.streamCh:
		l.mu.Lock()
		defer l.mu.Unlock()

		id := l.connID.Add(1)
		c := newConnection(id, s)
		l.connections[id] = c
		return nil, nil
	case <-l.ctx.Done():
		return nil, l.ctx.Err()
	}
}

// Close closes the listener.
func (l *listener) Close() error {
	l.cancel()
	return nil
}

// Addr returns the address of this listener.
func (l *listener) Addr() net.Addr {
	return nil
}
