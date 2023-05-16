package quicreuse

import (
	"net"
	"sync"
	"time"

	"github.com/google/gopacket/routing"
	"github.com/libp2p/go-netroute"
)

type pConn interface {
	net.PacketConn

	// count conn reference
	DecreaseCount()
	IncreaseCount()
}

type noreuseConn struct {
	*net.UDPConn
}

func (c *noreuseConn) IncreaseCount() {}
func (c *noreuseConn) DecreaseCount() {
	c.UDPConn.Close()
}

// Constant. Defined as variables to simplify testing.
var (
	garbageCollectInterval = 30 * time.Second
	maxUnusedDuration      = 10 * time.Second
)

type reuseConn struct {
	*net.UDPConn

	mutex       sync.Mutex
	refCount    int
	unusedSince time.Time
}

func newReuseConn(conn *net.UDPConn) *reuseConn {
	return &reuseConn{UDPConn: conn}
}

func (c *reuseConn) IncreaseCount() {
	c.mutex.Lock()
	c.refCount++
	c.unusedSince = time.Time{}
	c.mutex.Unlock()
}

func (c *reuseConn) DecreaseCount() {
	c.mutex.Lock()
	c.refCount--
	if c.refCount == 0 {
		c.unusedSince = time.Now()
	}
	c.mutex.Unlock()
}

func (c *reuseConn) ShouldGarbageCollect(now time.Time) bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return !c.unusedSince.IsZero() && c.unusedSince.Add(maxUnusedDuration).Before(now)
}

type reuse struct {
	mutex sync.Mutex

	closeChan  chan struct{}
	gcStopChan chan struct{}

	routes  routing.Router
	unicast map[string] /* IP.String() */ map[int] /* port */ *reuseConn
	// globalListeners contains connections that are listening on 0.0.0.0 / ::
	globalListeners map[int]*reuseConn
	// globalDialers contains connections that we've dialed out from. These connections are listening on 0.0.0.0 / ::
	// On Dial, connections are reused from this map if no connection is available in the globalListeners
	// On Listen, connections are reused from this map if the requested port is 0, and then moved to globalListeners
	globalDialers map[int]*reuseConn
}

func newReuse() *reuse {
	r := &reuse{
		unicast:         make(map[string]map[int]*reuseConn),
		globalListeners: make(map[int]*reuseConn),
		globalDialers:   make(map[int]*reuseConn),
		closeChan:       make(chan struct{}),
		gcStopChan:      make(chan struct{}),
	}
	go r.gc()
	return r
}

func (r *reuse) gc() {
	defer func() {
		r.mutex.Lock()
		for _, conn := range r.globalListeners {
			conn.Close()
		}
		for _, conn := range r.globalDialers {
			conn.Close()
		}
		for _, conns := range r.unicast {
			for _, conn := range conns {
				conn.Close()
			}
		}
		r.mutex.Unlock()
		close(r.gcStopChan)
	}()
	ticker := time.NewTicker(garbageCollectInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.closeChan:
			return
		case <-ticker.C:
			now := time.Now()
			r.mutex.Lock()
			for key, conn := range r.globalListeners {
				if conn.ShouldGarbageCollect(now) {
					conn.Close()
					delete(r.globalListeners, key)
				}
			}
			for key, conn := range r.globalDialers {
				if conn.ShouldGarbageCollect(now) {
					conn.Close()
					delete(r.globalDialers, key)
				}
			}
			for ukey, conns := range r.unicast {
				for key, conn := range conns {
					if conn.ShouldGarbageCollect(now) {
						conn.Close()
						delete(conns, key)
					}
				}
				if len(conns) == 0 {
					delete(r.unicast, ukey)
					// If we've dropped all connections with a unicast binding,
					// assume our routes may have changed.
					if len(r.unicast) == 0 {
						r.routes = nil
					} else {
						// Ignore the error, there's nothing we can do about
						// it.
						r.routes, _ = netroute.New()
					}
				}
			}
			r.mutex.Unlock()
		}
	}
}

func (r *reuse) Dial(network string, raddr *net.UDPAddr) (*reuseConn, error) {
	var ip *net.IP

	// Only bother looking up the source address if we actually _have_ non 0.0.0.0 listeners.
	// Otherwise, save some time.

	r.mutex.Lock()
	router := r.routes
	r.mutex.Unlock()

	if router != nil {
		_, _, src, err := router.Route(raddr.IP)
		if err == nil && !src.IsUnspecified() {
			ip = &src
		}
	}

	r.mutex.Lock()
	defer r.mutex.Unlock()

	conn, err := r.dialLocked(network, ip)
	if err != nil {
		return nil, err
	}
	conn.IncreaseCount()
	return conn, nil
}

func (r *reuse) dialLocked(network string, source *net.IP) (*reuseConn, error) {
	if source != nil {
		// We already have at least one suitable connection...
		if conns, ok := r.unicast[source.String()]; ok {
			// ... we don't care which port we're dialing from. Just use the first.
			for _, c := range conns {
				return c, nil
			}
		}
	}

	// Use a connection listening on 0.0.0.0 (or ::).
	// Again, we don't care about the port number.
	for _, conn := range r.globalListeners {
		return conn, nil
	}

	// Use a connection we've previously dialed from
	for _, conn := range r.globalDialers {
		return conn, nil
	}

	// We don't have a connection that we can use for dialing.
	// Dial a new connection from a random port.
	var addr *net.UDPAddr
	switch network {
	case "udp4":
		addr = &net.UDPAddr{IP: net.IPv4zero, Port: 0}
	case "udp6":
		addr = &net.UDPAddr{IP: net.IPv6zero, Port: 0}
	}
	conn, err := net.ListenUDP(network, addr)
	if err != nil {
		return nil, err
	}
	rconn := newReuseConn(conn)
	r.globalDialers[conn.LocalAddr().(*net.UDPAddr).Port] = rconn
	return rconn, nil
}

func (r *reuse) Listen(network string, laddr *net.UDPAddr) (*reuseConn, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	// Check if we can reuse a connection we have already dialed out from.
	// We reuse a connection from globalDialers when the requested port is 0 or the requested
	// port is already in the globalDialers.
	// If we are reusing a connection from globalDialers, we move the globalDialers entry to
	// globalListeners
	if laddr.IP.IsUnspecified() {
		var rconn *reuseConn
		var localAddr *net.UDPAddr

		if laddr.Port == 0 {
			// the requested port is 0, we can reuse any connection
			for _, conn := range r.globalDialers {
				rconn = conn
				localAddr = rconn.UDPConn.LocalAddr().(*net.UDPAddr)
				delete(r.globalDialers, localAddr.Port)
				break
			}
		} else if _, ok := r.globalDialers[laddr.Port]; ok {
			rconn = r.globalDialers[laddr.Port]
			localAddr = rconn.UDPConn.LocalAddr().(*net.UDPAddr)
			delete(r.globalDialers, localAddr.Port)
		}
		// found a match
		if rconn != nil {
			rconn.IncreaseCount()
			r.globalListeners[localAddr.Port] = rconn
			return rconn, nil
		}
	}

	conn, err := net.ListenUDP(network, laddr)
	if err != nil {
		return nil, err
	}
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	rconn := newReuseConn(conn)

	rconn.IncreaseCount()

	// Deal with listen on a global address
	if localAddr.IP.IsUnspecified() {
		// The kernel already checked that the laddr is not already listen
		// so we need not check here (when we create ListenUDP).
		r.globalListeners[localAddr.Port] = rconn
		return rconn, nil
	}

	// Deal with listen on a unicast address
	if _, ok := r.unicast[localAddr.IP.String()]; !ok {
		r.unicast[localAddr.IP.String()] = make(map[int]*reuseConn)
		// Assume the system's routes may have changed if we're adding a new listener.
		// Ignore the error, there's nothing we can do.
		r.routes, _ = netroute.New()
	}

	// The kernel already checked that the laddr is not already listen
	// so we need not check here (when we create ListenUDP).
	r.unicast[localAddr.IP.String()][localAddr.Port] = rconn
	return rconn, nil
}

func (r *reuse) Close() error {
	close(r.closeChan)
	<-r.gcStopChan
	return nil
}
