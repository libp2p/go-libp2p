package memory

import (
	"context"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
	"sync"
	"sync/atomic"
)

type transport struct {
	rcmgr network.ResourceManager

	mu sync.RWMutex

	connID      atomic.Int32
	listeners   map[ma.Multiaddr]*listener
	connections map[int32]*conn
}

func NewTransport() *transport {
	return &transport{
		connections: make(map[int32]*conn),
	}
}

func (t *transport) Dial(ctx context.Context, raddr ma.Multiaddr, p peer.ID) (tpt.CapableConn, error) {
	scope, err := t.rcmgr.OpenConnection(network.DirOutbound, false, raddr)
	if err != nil {
		return nil, err
	}

	c, err := t.dialWithScope(ctx, raddr, p, scope)
	if err != nil {
		return nil, err
	}

	return c, nil
}

func (t *transport) dialWithScope(ctx context.Context, raddr ma.Multiaddr, p peer.ID, scope network.ConnManagementScope) (tpt.CapableConn, error) {
	if err := scope.SetPeer(p); err != nil {
		return nil, err
	}

	// TODO: Check if there is an existing listener for this address
	t.mu.RLock()
	defer t.mu.RUnlock()
	l := t.listeners[raddr]

	in := make(chan []byte)
	out := make(chan []byte)
	s := newStream(0, in, out)
	l.streamCh <- s

	return newConnection(0, s), nil
}

func (t *transport) CanDial(addr ma.Multiaddr) bool {
	return true
}

func (t *transport) Listen(laddr ma.Multiaddr) (tpt.Listener, error) {
	// TODO: Figure out correct channel type
	return newListener(laddr, nil), nil
}

func (t *transport) Proxy() bool {
	return false
}

// Protocols returns the set of protocols handled by this transport.
func (t *transport) Protocols() []int {
	return []int{777}
}

func (t *transport) String() string {
	return "MemoryTransport"
}

func (t *transport) Close() error {
	// TODO: Go trough all listeners and close them
	return nil
}

func (t *transport) addConn(c *conn) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.connections[c.id] = c
}

func (t *transport) removeConn(c *conn) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.connections, c.id)
}
