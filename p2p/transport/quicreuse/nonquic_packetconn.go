package quicreuse

import (
	"context"
	"net"
	"time"
)

// nonQUICPacketConn is a net.PacketConn that can be used to read and write
// non-QUIC packets on a quic.Transport. This lets us reuse this UDP port for
// other transports like WebRTC.
type nonQUICPacketConn struct {
	owningTransport refCountedQuicTransport
	tr              QUICTransport
	ctx             context.Context
	ctxCancel       context.CancelFunc
	readCtx         context.Context
	readCancel      context.CancelFunc
}

// Close implements net.PacketConn.
func (n *nonQUICPacketConn) Close() error {
	n.ctxCancel()

	// Don't actually close the underlying transport since someone else might be using it.
	// reuse has it's own gc to close unused transports.
	n.owningTransport.DecreaseCount()
	return nil
}

// LocalAddr implements net.PacketConn.
func (n *nonQUICPacketConn) LocalAddr() net.Addr {
	return n.owningTransport.LocalAddr()
}

// ReadFrom implements net.PacketConn.
func (n *nonQUICPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	ctx := n.readCtx
	if ctx == nil {
		ctx = n.ctx
	}
	return n.tr.ReadNonQUICPacket(ctx, p)
}

// SetDeadline implements net.PacketConn.
func (n *nonQUICPacketConn) SetDeadline(t time.Time) error {
	// Only used for reads.
	return n.SetReadDeadline(t)
}

// SetReadDeadline implements net.PacketConn.
func (n *nonQUICPacketConn) SetReadDeadline(t time.Time) error {
	if t.IsZero() && n.readCtx != nil {
		n.readCancel()
		n.readCtx = nil
	}
	n.readCtx, n.readCancel = context.WithDeadline(n.ctx, t)
	return nil
}

// SetWriteDeadline implements net.PacketConn.
func (n *nonQUICPacketConn) SetWriteDeadline(t time.Time) error {
	// Unused. quic-go doesn't support deadlines for writes.
	return nil
}

// WriteTo implements net.PacketConn.
func (n *nonQUICPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	return n.tr.WriteTo(p, addr)
}

var _ net.PacketConn = &nonQUICPacketConn{}
