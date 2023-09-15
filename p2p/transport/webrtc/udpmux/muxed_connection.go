package udpmux

import (
	"context"
	"errors"
	"net"
	"time"
)

var _ net.PacketConn = &muxedConnection{}

const queueLen = 128

// muxedConnection provides a net.PacketConn abstraction
// over packetQueue and adds the ability to store addresses
// from which this connection (indexed by ufrag) received
// data.
type muxedConnection struct {
	ctx     context.Context
	cancel  context.CancelFunc
	onClose func()
	queue   chan []byte
	remote  net.Addr
	mux     *UDPMux
}

var _ net.PacketConn = &muxedConnection{}

func newMuxedConnection(mux *UDPMux, onClose func(), remote net.Addr) *muxedConnection {
	ctx, cancel := context.WithCancel(mux.ctx)
	return &muxedConnection{
		ctx:     ctx,
		cancel:  cancel,
		queue:   make(chan []byte, queueLen),
		onClose: onClose,
		remote:  remote,
		mux:     mux,
	}
}

func (c *muxedConnection) Push(buf []byte) error {
	select {
	case <-c.ctx.Done():
		return errors.New("closed")
	default:
	}
	select {
	case c.queue <- buf:
		return nil
	default:
		return errors.New("queue full")
	}
}

func (c *muxedConnection) ReadFrom(p []byte) (int, net.Addr, error) {
	select {
	case buf := <-c.queue:
		n := copy(p, buf) // This might discard parts of the packet, if p is too short
		if n < len(buf) {
			log.Debugf("short read, had %d, read %d", len(buf), n)
		}
		return n, c.remote, nil
	case <-c.ctx.Done():
		return 0, nil, c.ctx.Err()
	}
}

func (c *muxedConnection) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	return c.mux.writeTo(p, addr)
}

func (c *muxedConnection) Close() error {
	select {
	case <-c.ctx.Done():
		return nil
	default:
	}
	c.onClose()
	c.cancel()
	// drain the packet queue
	for {
		select {
		case <-c.queue:
		default:
			return nil
		}
	}
}

func (c *muxedConnection) LocalAddr() net.Addr  { return c.mux.socket.LocalAddr() }
func (c *muxedConnection) RemoteAddr() net.Addr { return c.remote }

func (*muxedConnection) SetDeadline(t time.Time) error {
	// no deadline is desired here
	return nil
}

func (*muxedConnection) SetReadDeadline(t time.Time) error {
	// no read deadline is desired here
	return nil
}

func (*muxedConnection) SetWriteDeadline(t time.Time) error {
	// no write deadline is desired here
	return nil
}
