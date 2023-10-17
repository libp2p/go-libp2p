package libp2pwebrtc

import (
	"fmt"
	"net"
	"sync"

	"github.com/libp2p/go-libp2p/p2p/transport/webrtc/udpmux"
	"github.com/libp2p/go-netroute"
)

// reuseUDPMux provides ability to reuse listening udpMux for dialing. This helps with address
// discovery for nodes that don't have access to their public ip address
type reuseUDPMux struct {
	mu          sync.RWMutex
	loopback    map[int]*udpmux.UDPMux
	specific    map[string]map[int]*udpmux.UDPMux // IP.String() => Port => Mux
	unspecified map[int]*udpmux.UDPMux
}

// Put stores mux for reuse later in Get calls.
func (r *reuseUDPMux) Put(mux *udpmux.UDPMux) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range mux.GetListenAddresses() {
		udpAddr, err := net.ResolveUDPAddr(a.Network(), a.String())
		if err != nil {
			return fmt.Errorf("udpmux ResolveUDPAddr failed for %s: %w", a, err)
		}
		if udpAddr.IP.IsLoopback() {
			r.loopback[udpAddr.Port] = mux
			continue
		}
		if udpAddr.IP.IsUnspecified() {
			r.unspecified[udpAddr.Port] = mux
			continue
		}
		if r.specific[udpAddr.IP.String()] == nil {
			r.specific[udpAddr.IP.String()] = make(map[int]*udpmux.UDPMux)
		}
		r.specific[udpAddr.IP.String()][udpAddr.Port] = mux
	}
	return nil
}

// Get retrieves a mux capable of dialing addr. Returns nil if no capable mux is present. If
// multiple muxes capable of dialing addr are available, it returns one arbitrarily
func (r *reuseUDPMux) Get(addr *net.UDPAddr) *udpmux.UDPMux {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if addr.IP.IsLoopback() {
		for _, m := range r.loopback {
			return m
		}
	}
	if len(r.specific) > 0 {
		if router, err := netroute.New(); err == nil {
			if _, _, preferredSrc, err := router.Route(addr.IP); err == nil {
				if len(r.specific[preferredSrc.String()]) != 0 {
					for _, m := range r.specific[preferredSrc.String()] {
						return m
					}
				}
			}
		}
	}
	for _, m := range r.unspecified {
		return m
	}
	return nil
}

// Delete removes a mux from the reuse pool.
func (r *reuseUDPMux) Delete(mux *udpmux.UDPMux) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for p, m := range r.loopback {
		if mux == m {
			delete(r.loopback, p)
		}
	}
	for p, m := range r.unspecified {
		if mux == m {
			delete(r.unspecified, p)
		}
	}
	for ip, mp := range r.specific {
		for p, m := range mp {
			if m == mux {
				delete(mp, p)
			}
		}
		if len(mp) == 0 {
			delete(r.specific, ip)
		}
	}
}
