package mocknet

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"

	process "github.com/jbenet/goprocess"
)

// link implements mocknet.Link
// and, for simplicity, network.Conn
type link struct {
	mock        *mocknet
	nets        []*peernet
	opts        LinkOptions
	ratelimiter *RateLimiter
	// this could have addresses on both sides.

	sync.RWMutex
}

func newLink(mn *mocknet, opts LinkOptions) *link {
	l := &link{mock: mn,
		opts:        opts,
		ratelimiter: NewRateLimiter(opts.Bandwidth)}
	return l
}

func (l *link) newConnPair(dialer *peernet) (*conn, *conn) {
	l.RLock()
	defer l.RUnlock()

	parent := process.WithTeardown(func() error { return nil })
	c1 := newConn(parent, l.nets[0], l.nets[1], l, network.DirOutbound)
	c2 := newConn(parent, l.nets[1], l.nets[0], l, network.DirInbound)
	c1.rconn = c2
	c2.rconn = c1

	if dialer == c1.net {
		return c1, c2
	}
	return c2, c1
}

func (l *link) Networks() []network.Network {
	l.RLock()
	defer l.RUnlock()

	cp := make([]network.Network, len(l.nets))
	for i, n := range l.nets {
		cp[i] = n
	}
	return cp
}

func (l *link) Peers() []peer.ID {
	l.RLock()
	defer l.RUnlock()

	cp := make([]peer.ID, len(l.nets))
	for i, n := range l.nets {
		cp[i] = n.peer
	}
	return cp
}

func (l *link) SetOptions(o LinkOptions) {
	l.Lock()
	defer l.Unlock()
	l.opts = o
	l.ratelimiter.UpdateBandwidth(l.opts.Bandwidth)
}

func (l *link) Options() LinkOptions {
	l.RLock()
	defer l.RUnlock()
	return l.opts
}

func (l *link) GetLatency() time.Duration {
	l.RLock()
	defer l.RUnlock()
	return l.opts.Latency
}

func (l *link) RateLimit(dataSize int) time.Duration {
	return l.ratelimiter.Limit(dataSize)
}
