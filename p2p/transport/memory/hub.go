package memory

import (
	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	mafmt "github.com/multiformats/go-multiaddr-fmt"
	"sync"
	"sync/atomic"
)

var (
	connCounter     atomic.Int64
	streamCounter   atomic.Int64
	listenerCounter atomic.Int64
	dialMatcher     = mafmt.Base(ma.P_MEMORY)
	memhub          = newHub()
)

type hub struct {
	mu        sync.RWMutex
	closeOnce sync.Once
	pubKeys   map[peer.ID]ic.PubKey
	listeners map[string]*listener
}

func newHub() *hub {
	return &hub{
		pubKeys:   make(map[peer.ID]ic.PubKey),
		listeners: make(map[string]*listener),
	}
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
