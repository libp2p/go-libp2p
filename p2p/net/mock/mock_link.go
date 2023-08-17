package mocknet

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
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
	l := &link{
		mock:        mn,
		opts:        opts,
		ratelimiter: NewRateLimiter(opts.Bandwidth),
	}
	return l
}

func (l *link) newConnPair(dialer *peernet) (*conn, *conn) {
	l.RLock()
	defer l.RUnlock()

	target := l.nets[0]
	if target == dialer {
		target = l.nets[1]
	}
	dc := newConn(dialer, target, l, network.DirOutbound)
	tc := newConn(target, dialer, l, network.DirInbound)
	dc.rconn = tc
	tc.rconn = dc
	return dc, tc
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
