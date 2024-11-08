package memory

import (
	"context"
	"net"
	"sync"

	tpt "github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
)

const (
	listenerQueueSize = 16
)

type listener struct {
	t      *transport
	ctx    context.Context
	cancel context.CancelFunc
	laddr  ma.Multiaddr

	mu          sync.Mutex
	connCh      chan *conn
	connections map[int32]*conn
}

func (l *listener) Multiaddr() ma.Multiaddr {
	return l.laddr
}

func newListener(t *transport, laddr ma.Multiaddr) *listener {
	ctx, cancel := context.WithCancel(context.Background())
	return &listener{
		t:           t,
		ctx:         ctx,
		cancel:      cancel,
		laddr:       laddr,
		connCh:      make(chan *conn, listenerQueueSize),
		connections: make(map[int32]*conn),
	}
}

// Accept accepts new connections.
func (l *listener) Accept() (tpt.CapableConn, error) {
	select {
	case <-l.ctx.Done():
		return nil, tpt.ErrListenerClosed
	case c, ok := <-l.connCh:
		if !ok {
			return nil, tpt.ErrListenerClosed
		}

		l.mu.Lock()
		defer l.mu.Unlock()

		l.connections[c.id] = c
		return c, nil
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
