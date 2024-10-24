package memory

import (
	"context"
	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/pnet"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
	"io"
	"sync"
	"sync/atomic"
)

const (
	listenerQueueSize = 16
)

type transport struct {
	pkey  ic.PrivKey
	pid   peer.ID
	psk   pnet.PSK
	rcmgr network.ResourceManager

	mu sync.RWMutex

	connID      atomic.Int32
	listeners   map[string]*listener
	connections map[int32]*conn
}

func NewTransport(key ic.PrivKey, psk pnet.PSK, rcmgr network.ResourceManager) (tpt.Transport, error) {
	if rcmgr == nil {
		rcmgr = &network.NullResourceManager{}
	}

	id, err := peer.IDFromPrivateKey(key)
	if err != nil {
		return nil, err
	}

	return &transport{
		rcmgr:       rcmgr,
		pid:         id,
		pkey:        key,
		psk:         psk,
		listeners:   make(map[string]*listener),
		connections: make(map[int32]*conn),
	}, nil
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
	l := t.listeners[raddr.String()]

	ra, wb := io.Pipe()
	rb, wa := io.Pipe()
	in, out := newStream(0, ra, wb), newStream(0, rb, wa)
	inId, outId := t.connID.Add(1), t.connID.Add(1)

	l.connCh <- newConnection(inId, in)

	return newConnection(outId, out), nil
}

func (t *transport) CanDial(addr ma.Multiaddr) bool {
	return true
}

func (t *transport) Listen(laddr ma.Multiaddr) (tpt.Listener, error) {
	// TODO: Check if we need to add scope via conn mngr
	l := newListener(t, laddr)

	t.mu.Lock()
	defer t.mu.Unlock()

	t.listeners[laddr.String()] = l

	return l, nil
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
	for _, l := range t.listeners {
		l.Close()
	}

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
