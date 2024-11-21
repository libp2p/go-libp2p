package memory

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/pnet"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	ma "github.com/multiformats/go-multiaddr"
	mafmt "github.com/multiformats/go-multiaddr-fmt"
)

var (
	connCounter     atomic.Int64
	streamCounter   atomic.Int64
	listenerCounter atomic.Int64
	dialMatcher     = mafmt.Base(ma.P_MEMORY)
)

type hub struct {
	mu        sync.RWMutex
	closeOnce sync.Once
	pubKeys   map[peer.ID]ic.PubKey
	listeners map[string]*listener
}

func (h *hub) addListener(addr string, l *listener) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.listeners[addr] = l
}

func (h *hub) removeListener(addr string, l *listener) {
	h.mu.Lock()
	defer h.mu.Unlock()

	delete(h.listeners, addr)
}

func (h *hub) getListener(addr string) (*listener, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	l, ok := h.listeners[addr]
	return l, ok
}

func (h *hub) addPubKey(p peer.ID, pk ic.PubKey) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.pubKeys[p] = pk
}

func (h *hub) getPubKey(p peer.ID) (ic.PubKey, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	pk, ok := h.pubKeys[p]
	return pk, ok
}

func (h *hub) close() {
	h.closeOnce.Do(func() {
		h.mu.Lock()
		defer h.mu.Unlock()

		for _, l := range h.listeners {
			l.Close()
		}
	})
}

var memhub = &hub{
	listeners: make(map[string]*listener),
	pubKeys:   make(map[peer.ID]ic.PubKey),
}

type transport struct {
	psk          pnet.PSK
	rcmgr        network.ResourceManager
	localPeerID  peer.ID
	localPrivKey ic.PrivKey
	localPubKey  ic.PubKey

	mu sync.RWMutex

	connections map[int64]*conn
}

func NewTransport(privKey ic.PrivKey, psk pnet.PSK, rcmgr network.ResourceManager) (tpt.Transport, error) {
	if rcmgr == nil {
		rcmgr = &network.NullResourceManager{}
	}

	id, err := peer.IDFromPrivateKey(privKey)
	if err != nil {
		return nil, err
	}

	memhub.addPubKey(id, privKey.GetPublic())
	return &transport{
		psk:          psk,
		rcmgr:        rcmgr,
		localPeerID:  id,
		localPrivKey: privKey,
		localPubKey:  privKey.GetPublic(),
		connections:  make(map[int64]*conn),
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

func (t *transport) dialWithScope(ctx context.Context, raddr ma.Multiaddr, rpid peer.ID, scope network.ConnManagementScope) (tpt.CapableConn, error) {
	if err := scope.SetPeer(rpid); err != nil {
		return nil, err
	}

	// TODO: Check if there is an existing listener for this address
	t.mu.RLock()
	defer t.mu.RUnlock()
	l, ok := memhub.getListener(raddr.String())
	if !ok {
		return nil, errors.New("failed to get listener")
	}

	remotePubKey, ok := memhub.getPubKey(rpid)
	if !ok {
		return nil, errors.New("failed to get remote public key")
	}

	sl, sr := newStreamPair()

	lconn := newConnection(t, sl, t.localPeerID, nil, remotePubKey, rpid, raddr)
	rconn := newConnection(nil, sr, rpid, raddr, t.localPubKey, t.localPeerID, nil)

	l.connCh <- rconn

	return lconn, nil
}

func (t *transport) CanDial(addr ma.Multiaddr) bool {
	return dialMatcher.Matches(addr)
}

func (t *transport) Listen(laddr ma.Multiaddr) (tpt.Listener, error) {
	// TODO: Check if we need to add scope via conn mngr
	l := newListener(t, laddr)

	t.mu.Lock()
	defer t.mu.Unlock()

	memhub.addListener(laddr.String(), l)

	return l, nil
}

func (t *transport) Proxy() bool {
	return false
}

// Protocols returns the set of protocols handled by this transport.
func (t *transport) Protocols() []int {
	return []int{ma.P_MEMORY}
}

func (t *transport) String() string {
	return "MemoryTransport"
}

func (t *transport) Close() error {
	// TODO: Go trough all listeners and close them
	//memhub.close()

	for _, c := range t.connections {
		c.Close()
		//delete(t.connections, c.id)
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
