package simconn

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

type PacketReciever interface {
	RecvPacket(p Packet)
}

// PerfectRouter is a router that has no latency or jitter and can route to
// every node
type PerfectRouter struct {
	mu    sync.Mutex
	nodes map[net.Addr]PacketReciever
}

// SendPacket implements Router.
func (r *PerfectRouter) SendPacket(p Packet) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	conn, ok := r.nodes[p.To]
	if !ok {
		return errors.New("unknown destination")
	}

	conn.RecvPacket(p)
	return nil
}

func (r *PerfectRouter) AddNode(addr net.Addr, conn PacketReciever) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.nodes == nil {
		r.nodes = make(map[net.Addr]PacketReciever)
	}
	r.nodes[addr] = conn
}

func (r *PerfectRouter) RemoveNode(addr net.Addr) {
	delete(r.nodes, addr)
}

var _ Router = &PerfectRouter{}

type DelayedPacketReciever struct {
	inner PacketReciever
	delay time.Duration
}

func (r *DelayedPacketReciever) RecvPacket(p Packet) {
	time.AfterFunc(r.delay, func() { r.inner.RecvPacket(p) })
}

type FixedLatencyRouter struct {
	PerfectRouter
	latency time.Duration
}

func (r *FixedLatencyRouter) SendPacket(p Packet) error {
	return r.PerfectRouter.SendPacket(p)
}

func (r *FixedLatencyRouter) AddNode(addr net.Addr, conn PacketReciever) {
	r.PerfectRouter.AddNode(addr, &DelayedPacketReciever{
		inner: conn,
		delay: r.latency,
	})
}

var _ Router = &FixedLatencyRouter{}

type simpleNodeFirewall struct {
	mu                sync.Mutex
	publiclyReachable bool
	packetsOutTo      map[string]struct{}
	node              *SimConn
}

func (f *simpleNodeFirewall) MarkPacketSentOut(p Packet) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.packetsOutTo == nil {
		f.packetsOutTo = make(map[string]struct{})
	}
	f.packetsOutTo[p.To.String()] = struct{}{}
}

func (f *simpleNodeFirewall) IsPacketInAllowed(p Packet) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.publiclyReachable {
		return true
	}

	_, ok := f.packetsOutTo[p.From.String()]
	return ok
}

func (f *simpleNodeFirewall) String() string {
	return fmt.Sprintf("public: %v, packetsOutTo: %v", f.publiclyReachable, f.packetsOutTo)
}

type SimpleFirewallRouter struct {
	mu    sync.Mutex
	nodes map[string]*simpleNodeFirewall
}

func (r *SimpleFirewallRouter) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	nodes := make([]string, 0, len(r.nodes))
	for _, node := range r.nodes {
		nodes = append(nodes, node.String())
	}
	return fmt.Sprintf("%v", nodes)
}

func (r *SimpleFirewallRouter) SendPacket(p Packet) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	toNode, exists := r.nodes[p.To.String()]
	if !exists {
		return errors.New("unknown destination")
	}

	// Record that this node is sending a packet to the destination
	fromNode, exists := r.nodes[p.From.String()]
	if !exists {
		return errors.New("unknown source")
	}
	fromNode.MarkPacketSentOut(p)

	if !toNode.IsPacketInAllowed(p) {
		return nil // Silently drop blocked packets
	}

	toNode.node.RecvPacket(p)
	return nil
}

func (r *SimpleFirewallRouter) AddNode(addr net.Addr, conn *SimConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.nodes == nil {
		r.nodes = make(map[string]*simpleNodeFirewall)
	}
	r.nodes[addr.String()] = &simpleNodeFirewall{
		packetsOutTo: make(map[string]struct{}),
		node:         conn,
	}
}

func (r *SimpleFirewallRouter) AddPubliclyReachableNode(addr net.Addr, conn *SimConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.nodes == nil {
		r.nodes = make(map[string]*simpleNodeFirewall)
	}
	r.nodes[addr.String()] = &simpleNodeFirewall{
		publiclyReachable: true,
		node:              conn,
	}
}

func (r *SimpleFirewallRouter) RemoveNode(addr net.Addr) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.nodes == nil {
		return
	}
	delete(r.nodes, addr.String())
}

var _ Router = &SimpleFirewallRouter{}
