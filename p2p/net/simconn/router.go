package simconn

import (
	"errors"
	"net"
	"sync"
	"time"
)

// PerfectRouter is a router that has no latency or jitter and can route to
// every node
type PerfectRouter struct {
	nodes map[net.Addr]*SimConn
}

// SendPacket implements Router.
func (r *PerfectRouter) SendPacket(deadline time.Time, p Packet) error {
	conn, ok := r.nodes[p.To]
	if !ok {
		return errors.New("unknown destination")
	}

	conn.RecvPacket(p)
	return nil
}

func (r *PerfectRouter) AddNode(addr net.Addr, conn *SimConn) {
	r.nodes[addr] = conn
}

func (r *PerfectRouter) RemoveNode(addr net.Addr) {
	delete(r.nodes, addr)
}

var _ Router = &PerfectRouter{}

type FixedLatencyRouter struct {
	PerfectRouter
	latency time.Duration
}

func (r *FixedLatencyRouter) SendPacket(deadline time.Time, p Packet) error {
	if !deadline.IsZero() {
		select {
		case <-time.After(r.latency):
		case <-time.After(time.Until(deadline)):
			return ErrDeadlineExceeded
		}
	} else {
		time.Sleep(r.latency)
	}
	return r.PerfectRouter.SendPacket(deadline, p)
}

var _ Router = &FixedLatencyRouter{}

type simpleNodeFirewall struct {
	mu           sync.Mutex
	public       bool
	packetsOutTo map[string]struct{}
	node         *SimConn
}

func (f *simpleNodeFirewall) IsPacketInAllowed(p Packet) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.public {
		return true
	}

	_, ok := f.packetsOutTo[p.From.String()]
	return ok
}

type SimpleFirewallRouter struct {
	nodes map[net.Addr]*simpleNodeFirewall
}

func (r *SimpleFirewallRouter) SendPacket(deadline time.Time, p Packet) error {
	toNode, exists := r.nodes[p.To]
	if !exists {
		return errors.New("unknown destination")
	}

	// Record that this node is sending a packet to the destination
	fromNode, exists := r.nodes[p.From]
	if !exists {
		return errors.New("unknown source")
	}
	fromNode.mu.Lock()
	fromNode.packetsOutTo[p.To.String()] = struct{}{}
	fromNode.mu.Unlock()

	if !toNode.IsPacketInAllowed(p) {
		return nil // Silently drop blocked packets
	}

	toNode.node.RecvPacket(p)
	return nil
}

func (r *SimpleFirewallRouter) AddNode(addr net.Addr, conn *SimConn) {
	r.nodes[addr] = &simpleNodeFirewall{
		packetsOutTo: make(map[string]struct{}),
		node:         conn,
	}
}

func (r *SimpleFirewallRouter) AddPublicNode(addr net.Addr, conn *SimConn) {
	r.nodes[addr] = &simpleNodeFirewall{
		public: true,
		node:   conn,
	}
}

func (r *SimpleFirewallRouter) RemoveNode(addr net.Addr) {
	delete(r.nodes, addr)
}

var _ Router = &SimpleFirewallRouter{}
